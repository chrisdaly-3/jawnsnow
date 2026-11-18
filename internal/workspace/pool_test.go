package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

func newPoolFixture(t *testing.T, size int) (scratch, Repo, *Pool, *fakeSlots, string) {
	t.Helper()
	s := newScratch(t)
	r, err := Resolve(context.Background(), "o/n", config.Repo{Path: s.clone}, t.TempDir(), noClone(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(realPath(t, t.TempDir()), "worktrees", "o__n")
	st := newFakeSlots()
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	p := NewPool(r, dir, size, st, func() time.Time { clock = clock.Add(time.Minute); return clock })
	return s, r, p, st, dir
}

func TestPoolReusesSlots(t *testing.T) {
	ctx := context.Background()
	s, r, p, st, dir := newPoolFixture(t, 2)
	head1, _, err := r.FetchPR(ctx, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	path1, release1, err := p.Acquire(ctx, 7, head1)
	if err != nil {
		t.Fatal(err)
	}
	if path1 != filepath.Join(dir, "slot-1") {
		t.Fatalf("path = %s", path1)
	}
	if got := git(t, path1, "rev-parse", "HEAD"); got != head1 {
		t.Fatalf("slot at %s, want %s", got, head1)
	}
	slots, _ := st.Slots(ctx, "o/n")
	if len(slots) != 1 || !slots[0].Busy || slots[0].PRNumber != 7 || slots[0].HeadOID != head1 {
		t.Fatalf("slots while held: %+v", slots)
	}
	release1()
	release1() // safe twice
	slots, _ = st.Slots(ctx, "o/n")
	if slots[0].Busy || slots[0].LastUsed.IsZero() {
		t.Fatalf("slot after release: %+v", slots[0])
	}

	head2 := s.addPRCommit(t, 8, "c.txt")
	if _, _, err := r.FetchPR(ctx, 8, "main"); err != nil {
		t.Fatal(err)
	}
	path2, release2, err := p.Acquire(ctx, 8, head2)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if path2 != path1 {
		t.Fatalf("slot not reused: %s vs %s", path2, path1)
	}
	if got := git(t, path2, "rev-parse", "HEAD"); got != head2 {
		t.Fatalf("slot at %s, want %s", got, head2)
	}
	if branch := git(t, path2, "branch", "--show-current"); branch != "" {
		t.Fatalf("slot is on branch %q, want detached", branch)
	}
}

func TestPoolBusy(t *testing.T) {
	ctx := context.Background()
	_, r, p, _, _ := newPoolFixture(t, 1)
	head, _, err := r.FetchPR(ctx, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := p.Acquire(ctx, 7, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Acquire(ctx, 7, head); !errors.Is(err, ErrPoolBusy) {
		t.Fatalf("err = %v, want ErrPoolBusy", err)
	}
	release()
	_, release, err = p.Acquire(ctx, 7, head)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release()
}

func TestPoolConcurrentAcquireAndFetch(t *testing.T) {
	ctx := context.Background()
	s, r, p, _, _ := newPoolFixture(t, 2)
	s.addPRCommit(t, 8, "c.txt")

	var wg sync.WaitGroup
	paths := make([]string, 2)
	errs := make([]error, 2)
	for i, n := range []int{7, 8} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			head, _, err := r.FetchPR(ctx, n, "main")
			if err != nil {
				errs[i] = err
				return
			}
			path, _, err := p.Acquire(ctx, n, head)
			paths[i], errs[i] = path, err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if paths[0] == paths[1] {
		t.Fatalf("both goroutines got %s", paths[0])
	}
}

func TestPoolNeverTouchesUserWorktrees(t *testing.T) {
	ctx := context.Background()
	s, r, p, _, dir := newPoolFixture(t, 1)
	user := filepath.Join(s.root, "user-wt")
	git(t, s.clone, "worktree", "add", "--detach", user, "HEAD")
	write(t, filepath.Join(user, "wip.txt"), "uncommitted\n")
	// A user worktree whose directory is gone (say, an unplugged drive)
	// must stay registered: pruning would drop it.
	gone := filepath.Join(s.root, "gone-wt")
	git(t, s.clone, "worktree", "add", "--detach", gone, "HEAD")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	userHead := git(t, user, "rev-parse", "HEAD")
	branches := git(t, s.clone, "for-each-ref", "refs/heads")

	head, _, err := r.FetchPR(ctx, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	path, release, err := p.Acquire(ctx, 7, head)
	if err != nil {
		t.Fatal(err)
	}
	release()
	// Lose the slot's directory, then acquire again.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	path, release, err = p.Acquire(ctx, 7, head)
	if err != nil {
		t.Fatalf("Acquire after the slot dir was deleted: %v", err)
	}
	release()
	if got := git(t, path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("recreated slot at %s", got)
	}
	if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Fatalf("slot %s outside %s", path, dir)
	}

	if got, err := os.ReadFile(filepath.Join(user, "wip.txt")); err != nil || string(got) != "uncommitted\n" {
		t.Fatalf("user worktree changed: %q, %v", got, err)
	}
	if got := git(t, user, "rev-parse", "HEAD"); got != userHead {
		t.Fatalf("user worktree HEAD moved to %s", got)
	}
	if got := git(t, s.clone, "for-each-ref", "refs/heads"); got != branches {
		t.Fatalf("user branches changed")
	}
	list := git(t, s.clone, "worktree", "list", "--porcelain")
	for _, want := range []string{user, gone} {
		if !strings.Contains(list, want) {
			t.Fatalf("worktree %s no longer registered:\n%s", want, list)
		}
	}
}
