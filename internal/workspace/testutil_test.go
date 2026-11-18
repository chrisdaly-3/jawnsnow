package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// git runs git in dir for test setup. The machine signs commits globally,
// so signing is switched off and an identity is supplied per call.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-C", dir,
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
		"-c", "init.defaultBranch=main",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// scratch is a set of throwaway repos: an upstream bare repo "o/n" carrying
// main and refs/pull/7/head, a fork of it, and a user clone whose origin is
// the fork and whose upstream remote is the base repo.
type scratch struct {
	root, upstream, fork, clone string
	mainSHA, prSHA              string
}

func newScratch(t *testing.T) scratch {
	t.Helper()
	root := realPath(t, t.TempDir())
	s := scratch{
		root:     root,
		upstream: filepath.Join(root, "o", "n.git"),
		fork:     filepath.Join(root, "fork", "n.git"),
		clone:    filepath.Join(root, "clone"),
	}
	work := filepath.Join(root, "work")
	for _, d := range []string{work, filepath.Join(root, "o"), filepath.Join(root, "fork")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, work, "init")
	write(t, filepath.Join(work, "a.txt"), "one\n")
	git(t, work, "add", "a.txt")
	git(t, work, "commit", "-m", "base")
	s.mainSHA = git(t, work, "rev-parse", "HEAD")

	git(t, root, "clone", "--bare", work, s.upstream)
	git(t, s.upstream, "config", "uploadpack.allowFilter", "true")

	git(t, work, "checkout", "-b", "pr")
	write(t, filepath.Join(work, "b.txt"), "two\n")
	git(t, work, "add", "b.txt")
	git(t, work, "commit", "-m", "pr change")
	s.prSHA = git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", s.upstream, "pr:refs/heads/pr-tmp")
	git(t, s.upstream, "update-ref", "refs/pull/7/head", s.prSHA)
	git(t, s.upstream, "update-ref", "-d", "refs/heads/pr-tmp")

	git(t, root, "clone", "--bare", s.upstream, s.fork)
	git(t, root, "clone", s.fork, s.clone)
	git(t, s.clone, "remote", "add", "upstream", s.upstream)
	return s
}

// addPRCommit puts another commit on upstream's refs/pull/<n>/head and
// returns its ID.
func (s scratch) addPRCommit(t *testing.T, n int, file string) string {
	t.Helper()
	work := filepath.Join(s.root, "work2")
	if _, err := os.Stat(work); err != nil {
		git(t, s.root, "clone", s.upstream, work)
	}
	git(t, work, "checkout", "-B", "pr"+file, s.mainSHA)
	write(t, filepath.Join(work, file), file+"\n")
	git(t, work, "add", file)
	git(t, work, "commit", "-m", "pr "+file)
	sha := git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", s.upstream, "HEAD:refs/heads/tmp-"+file)
	git(t, s.upstream, "update-ref", "refs/pull/"+itoa(n)+"/head", sha)
	git(t, s.upstream, "update-ref", "-d", "refs/heads/tmp-"+file)
	return sha
}

func itoa(n int) string { return strconv.Itoa(n) }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeSlots is an in-memory SlotStore.
type fakeSlots struct {
	mu    sync.Mutex
	slots map[string]store.Slot
}

func newFakeSlots() *fakeSlots { return &fakeSlots{slots: map[string]store.Slot{}} }

func (f *fakeSlots) Slots(_ context.Context, repo string) ([]store.Slot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Slot
	for _, s := range f.slots {
		if s.Repo == repo {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (f *fakeSlots) SaveSlot(_ context.Context, s store.Slot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots[s.Path] = s
	return nil
}

func (f *fakeSlots) DeleteSlot(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.slots, path)
	return nil
}
