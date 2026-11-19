package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cleanupFixture is a pool over a scratch clone that also holds things
// the user owns: a branch, a worktree outside the pool, and a worktree
// whose directory has been deleted. Cleanup must leave all three alone.
type cleanupFixture struct {
	s       scratch
	r       Repo
	p       *Pool
	st      *fakeSlots
	slots   string // the pool's slots directory
	data    string // the data directory: slots/../../
	userWT  string
	goneWT  string
	now     time.Time
	headOID string
}

func newCleanupFixture(t *testing.T, size int) cleanupFixture {
	t.Helper()
	s, r, _, st, slots := newPoolFixture(t, size)
	f := cleanupFixture{s: s, r: r, st: st, slots: slots, data: filepath.Dir(filepath.Dir(slots)), now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	f.p = NewPool(r, slots, size, st, func() time.Time { return f.now })

	git(t, s.clone, "branch", "userbranch")
	f.userWT = filepath.Join(s.root, "userwt")
	git(t, s.clone, "worktree", "add", f.userWT, "userbranch")
	f.goneWT = filepath.Join(s.root, "gonewt")
	git(t, s.clone, "worktree", "add", "--detach", f.goneWT)
	if err := os.RemoveAll(f.goneWT); err != nil {
		t.Fatal(err)
	}

	head, _, err := r.FetchPR(context.Background(), 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	f.headOID = head
	git(t, s.clone, "update-ref", "refs/jawnsnow/70/head", head)
	return f
}

func refNames(t *testing.T, dir, prefix string) string {
	t.Helper()
	return git(t, dir, "for-each-ref", "--format=%(refname)", prefix)
}

func worktreeList(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "worktree", "list", "--porcelain")
}

// assertUserStateIntact checks the user's branches, remotes and
// worktrees, including the one whose directory is missing.
func (f cleanupFixture) assertUserStateIntact(t *testing.T, heads string) {
	t.Helper()
	if got := refNames(t, f.s.clone, "refs/heads refs/remotes"); got != heads {
		t.Fatalf("user refs changed:\nbefore %q\nafter  %q", heads, got)
	}
	list := worktreeList(t, f.s.clone)
	for _, p := range []string{f.userWT, f.goneWT} {
		if !strings.Contains(list, "worktree "+p+"\n") {
			t.Fatalf("user worktree %s no longer registered:\n%s", p, list)
		}
	}
	if _, err := os.Stat(filepath.Join(f.userWT, "a.txt")); err != nil {
		t.Fatalf("user worktree files: %v", err)
	}
}

func (f cleanupFixture) userHeads(t *testing.T) string {
	return refNames(t, f.s.clone, "refs/heads refs/remotes")
}

func TestGitHardensEveryCommand(t *testing.T) {
	s := newScratch(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_SSH_COMMAND", "x")
	if err := os.Unsetenv("GIT_SSH_COMMAND"); err != nil {
		t.Fatal(err)
	}
	// A shell alias reports what a non-fetch command sees.
	out, err := Git(context.Background(), s.clone, "-c",
		`alias.probe=!printf '%s|%s|%s|%s' "$GIT_SSH_COMMAND" "$(git config core.hooksPath)" "$(git config submodule.recurse)" "$GIT_LFS_SKIP_SMUDGE"`, "probe")
	if err != nil {
		t.Fatal(err)
	}
	// A PR's .gitmodules or .lfsconfig must not make a checkout fetch.
	if got, want := string(out), "ssh -o BatchMode=yes|/dev/null|false|1"; got != want {
		t.Fatalf("probe = %q, want %q", got, want)
	}
	if _, err := Git(context.Background(), s.clone, "diff", "--quiet"); err != nil {
		t.Fatal(err)
	}
}

func TestReleasePRDeletesRefsAndDirs(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 2)
	heads := f.userHeads(t)

	path, _, err := f.p.Acquire(ctx, 7, f.headOID)
	if err != nil {
		t.Fatal(err)
	}
	review := filepath.Join(f.data, "reviews", "r1")
	if err := os.MkdirAll(review, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(review, "diff.txt"), "x")
	outside := filepath.Join(f.s.root, "precious")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	err = f.p.ReleasePR(ctx, 7, []string{review, outside, "relative/reviews/x", filepath.Join(f.data, "reviews")})
	if err == nil {
		t.Fatal("dirs outside reviews/ were accepted without error")
	}
	if _, err := os.Stat(review); !os.IsNotExist(err) {
		t.Fatalf("review dir remains: %v", err)
	}
	for _, p := range []string{outside, filepath.Join(f.data, "reviews")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
	got := refNames(t, f.s.clone, "refs/jawnsnow/")
	if strings.Contains(got, "refs/jawnsnow/7/") || !strings.Contains(got, "refs/jawnsnow/70/head") {
		t.Fatalf("refs after ReleasePR(7): %q", got)
	}
	slots, _ := f.st.Slots(ctx, "o/n")
	if len(slots) != 1 || slots[0].Path != path || slots[0].Busy || slots[0].PRNumber != 0 {
		t.Fatalf("slot after ReleasePR: %+v", slots)
	}
	// The slot is free again: a different PR can take it, and the stale
	// release func from the first Acquire does not free it from under them.
	if _, _, err := f.p.Acquire(ctx, 8, f.headOID); err != nil {
		t.Fatal(err)
	}
	f.assertUserStateIntact(t, heads)
}

func TestReleasePRStaleReleaseDoesNotFreeNewHolder(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 1)
	_, stale, err := f.p.Acquire(ctx, 7, f.headOID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.p.ReleasePR(ctx, 7, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.p.Acquire(ctx, 8, f.headOID); err != nil {
		t.Fatal(err)
	}
	stale()
	if _, _, err := f.p.Acquire(ctx, 9, f.headOID); err != ErrPoolBusy {
		t.Fatalf("slot handed to two holders: err=%v", err)
	}
}

func TestPruneIdleRemovesOnlyIdleFreeSlots(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 3)
	heads := f.userHeads(t)

	var paths [3]string
	var releases [3]func()
	for i := range paths {
		var err error
		paths[i], releases[i], err = f.p.Acquire(ctx, 7+i, f.headOID)
		if err != nil {
			t.Fatal(err)
		}
	}
	releases[0]()
	releases[1]()
	set := func(i int, age time.Duration) {
		slots, _ := f.st.Slots(ctx, "o/n")
		for _, s := range slots {
			if s.Path == paths[i] {
				s.LastUsed = f.now.Add(-age)
				if err := f.st.SaveSlot(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	set(0, 3*time.Hour) // free and old: removed
	set(1, 10*time.Minute)
	set(2, 5*time.Hour) // old but still held: kept

	if err := f.p.PruneIdle(ctx, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("idle slot remains: %v", err)
	}
	for _, p := range paths[1:] {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("slot %s removed: %v", p, err)
		}
	}
	if list := worktreeList(t, f.s.clone); strings.Contains(list, paths[0]) {
		t.Fatalf("idle slot still registered:\n%s", list)
	}
	slots, _ := f.st.Slots(ctx, "o/n")
	if len(slots) != 2 {
		t.Fatalf("store rows = %+v", slots)
	}
	f.assertUserStateIntact(t, heads)
}

func TestSweep(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 3)
	heads := f.userHeads(t)

	p1, rel1, _ := f.p.Acquire(ctx, 7, f.headOID)
	p2, rel2, _ := f.p.Acquire(ctx, 8, f.headOID)
	_ = rel1
	rel2()
	// p1 is still Busy in the store: the process "restarted" holding it.
	// p2's directory is deleted behind git's back.
	if err := os.RemoveAll(p2); err != nil {
		t.Fatal(err)
	}
	// A registered worktree in slots that the store doesn't know, a plain
	// directory, and a symlink to the user's worktree.
	stray := filepath.Join(f.slots, "stray")
	git(t, f.s.clone, "worktree", "add", "--detach", stray)
	orphan := filepath.Join(f.slots, "orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(orphan, "f"), "x")
	link := filepath.Join(f.slots, "link")
	if err := os.Symlink(f.userWT, link); err != nil {
		t.Fatal(err)
	}
	git(t, f.s.clone, "update-ref", "refs/jawnsnow/8/head", f.headOID)

	// A fresh pool: Sweep does not rely on this process's held set.
	if err := Sweep(ctx, f.r, f.slots, map[int]bool{7: true}, f.st); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{stray, orphan, p2} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s remains: %v", p, err)
		}
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("symlink remains: %v", err)
	}
	list := worktreeList(t, f.s.clone)
	if strings.Contains(list, stray) || strings.Contains(list, p2) || !strings.Contains(list, p1) {
		t.Errorf("registrations after Sweep:\n%s", list)
	}
	slots, _ := f.st.Slots(ctx, "o/n")
	if len(slots) != 1 || slots[0].Path != p1 || slots[0].Busy {
		t.Errorf("slots after Sweep: %+v", slots)
	}
	got := refNames(t, f.s.clone, "refs/jawnsnow/")
	if got != "refs/jawnsnow/7/base\nrefs/jawnsnow/7/head" {
		t.Errorf("jawnsnow refs after Sweep: %q", got)
	}
	f.assertUserStateIntact(t, heads)
}

func TestCleanupNeverTouchesUserState(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 2)
	heads := f.userHeads(t)
	_, rel, err := f.p.Acquire(ctx, 7, f.headOID)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	f.now = f.now.Add(5 * time.Hour)
	if err := f.p.PruneIdle(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := f.p.ReleasePR(ctx, 7, nil); err != nil {
		t.Fatal(err)
	}
	if err := Sweep(ctx, f.r, f.slots, nil, f.st); err != nil {
		t.Fatal(err)
	}
	if got := refNames(t, f.s.clone, "refs/jawnsnow/"); got != "" {
		t.Fatalf("jawnsnow refs remain: %q", got)
	}
	f.assertUserStateIntact(t, heads)
}

func TestSweepRejectsUnsafeSlotsDir(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 1)
	home := realPath(t, t.TempDir())
	t.Setenv("HOME", home)
	cwd := realPath(t, t.TempDir())
	t.Chdir(cwd)
	sentinels := []string{filepath.Join(cwd, "sentinel"), filepath.Join(home, "sentinel")}
	for _, p := range sentinels {
		write(t, p, "keep")
	}
	for _, dir := range []string{"", ".", "/", home, filepath.Join(f.data, "elsewhere", "o__n")} {
		if err := Sweep(ctx, f.r, dir, nil, f.st); err == nil {
			t.Errorf("Sweep(%q) succeeded", dir)
		}
	}
	for _, p := range sentinels {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sentinel %s gone: %v", p, err)
		}
	}
	p := NewPool(f.r, cwd, 1, f.st, time.Now)
	if p.PruneIdle(ctx, 0) == nil || p.ReleasePR(ctx, 7, nil) == nil {
		t.Error("pool over an unsafe dir did not refuse")
	}
}

func TestSweepKeepsDirWhenRemoveFails(t *testing.T) {
	ctx := context.Background()
	f := newCleanupFixture(t, 1)
	stray := filepath.Join(f.slots, "stray")
	git(t, f.s.clone, "worktree", "add", "--detach", stray)
	git(t, f.s.clone, "worktree", "lock", stray)
	err := Sweep(ctx, f.r, f.slots, nil, f.st)
	if err == nil {
		t.Fatal("Sweep hid the failed removal")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("directory removed although git kept the registration: %v", err)
	}
	if !strings.Contains(worktreeList(t, f.s.clone), stray) {
		t.Fatal("registration vanished")
	}
}
