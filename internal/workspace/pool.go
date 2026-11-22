package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// SlotStore persists worktree slots. *store.Store satisfies it.
type SlotStore interface {
	Slots(ctx context.Context, repo string) ([]store.Slot, error)
	SaveSlot(ctx context.Context, s store.Slot) error
	DeleteSlot(ctx context.Context, path string) error
}

var _ SlotStore = (*store.Store)(nil)

// ErrPoolBusy means every worktree slot is held.
var ErrPoolBusy = errors.New("no free worktree slot")

// Pool hands out reusable detached worktrees named slot-<n> under one
// directory. It only creates, moves and reuses worktrees inside that
// directory. Only cleanup.go removes worktrees, and only its own.
type Pool struct {
	repo Repo
	dir  string
	size int
	st   SlotStore
	now  func() time.Time

	mu   sync.Mutex
	held map[string]uint64 // slot path -> generation of its current holder; the truth for Busy
	gen  uint64
}

func NewPool(r Repo, dir string, size int, st SlotStore, now func() time.Time) *Pool {
	return &Pool{repo: r, dir: filepath.Clean(dir), size: size, st: st, now: now, held: map[string]uint64{}}
}

// Acquire checks head out, detached, in a free slot and returns its path.
// release frees the slot and is safe to call more than once.
func (p *Pool) Acquire(ctx context.Context, number int, head string) (string, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	slots, err := p.st.Slots(ctx, p.repo.Name)
	if err != nil {
		return "", nil, err
	}
	known := map[string]store.Slot{}
	for _, s := range slots {
		if filepath.Dir(s.Path) == p.dir {
			known[s.Path] = s
		}
	}

	chosen := p.chooseFree(known, number)
	switch {
	case chosen != "":
		err = p.checkout(ctx, chosen, head)
	case len(known) >= p.size:
		return "", nil, ErrPoolBusy
	default:
		chosen = p.newSlotPath(known)
		err = p.addWorktree(ctx, chosen, head)
	}
	if err != nil {
		return "", nil, err
	}

	slot := store.Slot{Repo: p.repo.Name, Path: chosen, PRNumber: number, HeadOID: head, LastUsed: p.now(), Busy: true}
	if err := p.st.SaveSlot(ctx, slot); err != nil {
		return "", nil, err
	}
	p.gen++
	gen := p.gen
	p.held[chosen] = gen

	var once sync.Once
	release := func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			// ReleasePR may have freed the slot and handed it to someone
			// else; that holder's state is not ours to touch.
			if p.held[chosen] != gen {
				return
			}
			delete(p.held, chosen)
			slot.Busy = false
			slot.LastUsed = p.now()
			// The caller's context may be cancelled by now, and a slot left
			// marked busy in the store would mislead the next run.
			_ = p.st.SaveSlot(context.Background(), slot)
		})
	}
	return chosen, release, nil
}

// chooseFree picks the free slot in known to reuse: the one that last held
// PR number, then the lowest path. It is "" when every slot is held.
func (p *Pool) chooseFree(known map[string]store.Slot, number int) string {
	var chosen string
	for path, s := range known {
		if _, ok := p.held[path]; ok {
			continue
		}
		if chosen == "" || (s.PRNumber == number && known[chosen].PRNumber != number) ||
			(s.PRNumber == number) == (known[chosen].PRNumber == number) && path < chosen {
			chosen = path
		}
	}
	return chosen
}

// newSlotPath is the path of the lowest-numbered slot-<n> that no slot in
// known has and no directory occupies.
func (p *Pool) newSlotPath(known map[string]store.Slot) string {
	for n := 1; ; n++ {
		path := filepath.Join(p.dir, fmt.Sprintf("slot-%d", n))
		if _, taken := known[path]; taken {
			continue
		}
		// A directory with no store row is an orphan from an
		// interrupted add; leave it alone and use the next number.
		if _, err := os.Lstat(path); err == nil {
			continue
		}
		return path
	}
}

// checkout moves an existing slot to head, re-creating the worktree when
// its directory has been deleted.
func (p *Pool) checkout(ctx context.Context, path, head string) error {
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return p.addWorktree(ctx, path, head)
	}
	if err := clearIndexLock(ctx, path); err != nil {
		return err
	}
	_, err := Git(ctx, path, "checkout", "--detach", "--force", head)
	return err
}

// clearIndexLock removes the index.lock a git killed mid-checkout left in
// the worktree's git directory; every later checkout would fail on it.
// The caller holds p.mu and the slot is not held, so no git of jawnSnow's
// is running there, and the instance lock rules out a second snow: any
// lock is stale.
func clearIndexLock(ctx context.Context, path string) error {
	out, err := Git(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(strings.TrimSpace(string(out)), "index.lock"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// addWorktree creates the worktree at path. --force lets git reuse a
// registration whose directory vanished, so no `git worktree prune` is
// needed; prune would also drop the user's own missing worktrees.
func (p *Pool) addWorktree(ctx context.Context, path, head string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := Git(ctx, p.repo.dir(), "worktree", "add", "--detach", "--force", path, head)
	return err
}
