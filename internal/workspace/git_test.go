package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

// noClone is a cloneURL for tests where the repo is mapped, so no clone
// happens. The URL does not exist: a clone attempt would fail.
func noClone(*testing.T) func(string) string {
	return func(string) string { return "/nonexistent/o/n.git" }
}

func TestResolveMappedPath(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	r, err := Resolve(ctx, "o/n", config.Repo{Path: s.clone}, t.TempDir(), noClone(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "o/n" || r.WorkDir != s.clone || r.GitDir != filepath.Join(s.clone, ".git") {
		t.Fatalf("got %+v", r)
	}

	// ~ expands against $HOME.
	t.Setenv("HOME", s.root)
	r, err = Resolve(ctx, "o/n", config.Repo{Path: "~/clone"}, t.TempDir(), noClone(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkDir != s.clone {
		t.Fatalf("~ not expanded: %+v", r)
	}
}

func TestResolveMakesBlobslessBareClone(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	dataDir := realPath(t, t.TempDir())
	url := func(string) string { return "file://" + s.upstream }

	r, err := Resolve(ctx, "o/n", config.Repo{}, dataDir, url)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "clones", "o__n.git")
	if r.GitDir != want || r.WorkDir != "" {
		t.Fatalf("got %+v, want GitDir %s", r, want)
	}
	if got := git(t, want, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("is-bare = %q", got)
	}
	if got := git(t, want, "config", "--get", "remote.origin.partialclonefilter"); got != "blob:none" {
		t.Fatalf("filter = %q", got)
	}

	// A second Resolve reuses the clone: a clone from this URL would fail.
	r2, err := Resolve(ctx, "o/n", config.Repo{}, dataDir, noClone(t))
	if err != nil {
		t.Fatalf("second Resolve re-cloned: %v", err)
	}
	if r2.GitDir != want {
		t.Fatalf("second GitDir = %s", r2.GitDir)
	}
}

// A mistyped repos.<name>.path is an error: cloning in its place would
// review a copy the user didn't mean.
func TestResolveBadMappedPathErrors(t *testing.T) {
	s := newScratch(t)
	dataDir := realPath(t, t.TempDir())
	notRepo := t.TempDir()
	for _, p := range []string{filepath.Join(s.root, "missing"), notRepo} {
		_, err := Resolve(context.Background(), "o/n", config.Repo{Path: p}, dataDir, func(string) string { return "file://" + s.upstream })
		want := `repos."o/n".path ` + p + " is not a git repository"
		if err == nil || err.Error() != want {
			t.Errorf("Resolve(%s) err = %v, want %q", p, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "clones")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a bad mapped path made %s/clones (stat err %v)", dataDir, err)
	}
}

func TestResolveRejectsBadName(t *testing.T) {
	for _, name := range []string{"", "x", "../a/b", "a/b/c", "-o/n", "o/.."} {
		if _, err := Resolve(context.Background(), name, config.Repo{}, t.TempDir(), func(string) string { return "x" }); err == nil {
			t.Errorf("Resolve(%q) succeeded", name)
		}
	}
}

func TestRemoteForMatchesBaseNotFork(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	r := Repo{Name: "o/n", GitDir: filepath.Join(s.clone, ".git"), WorkDir: s.clone}

	got, err := r.RemoteFor(ctx)
	if err != nil || got != "upstream" {
		t.Fatalf("RemoteFor = %q, %v; want upstream", got, err)
	}

	for _, u := range []string{
		"git@github.com:O/N.git",
		"git@github.com:o/n",
		"ssh://git@github.com/o/n.git",
		"https://github.com/o/n.git",
		"https://GitHub.com/O/N",
		"https://github.com/o/n/",
	} {
		git(t, s.clone, "remote", "set-url", "upstream", u)
		got, err := r.RemoteFor(ctx)
		if err != nil || got != "upstream" {
			t.Errorf("url %q: RemoteFor = %q, %v", u, got, err)
		}
	}

	// Only the fork is left: no remote is the base repo.
	git(t, s.clone, "remote", "remove", "upstream")
	got, err = r.RemoteFor(ctx)
	if err != nil || got != "" {
		t.Fatalf("fork only: RemoteFor = %q, %v; want empty", got, err)
	}
}

func TestFetchPRWritesJawnsnowRefs(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	r, err := Resolve(ctx, "o/n", config.Repo{Path: s.clone}, t.TempDir(), noClone(t))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := userRefs(t, s.clone)

	head, base, err := r.FetchPR(ctx, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	if head != s.prSHA || base != s.mainSHA {
		t.Fatalf("head=%s base=%s, want %s %s", head, base, s.prSHA, s.mainSHA)
	}
	if got := git(t, s.clone, "rev-parse", "refs/jawnsnow/7/head"); got != s.prSHA {
		t.Fatalf("refs/jawnsnow/7/head = %s", got)
	}
	if got := git(t, s.clone, "rev-parse", "refs/jawnsnow/7/base"); got != s.mainSHA {
		t.Fatalf("refs/jawnsnow/7/base = %s", got)
	}
	if got := userRefs(t, s.clone); got != refsBefore {
		t.Fatalf("refs outside refs/jawnsnow changed:\n%s\n->\n%s", refsBefore, got)
	}

	mb, err := r.MergeBase(ctx, head, base)
	if err != nil || mb != s.mainSHA {
		t.Fatalf("MergeBase = %q, %v", mb, err)
	}
}

func TestFetchPRFallsBackToCloneURL(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	// The user's clone only knows the fork, so no remote is the base repo.
	git(t, s.clone, "remote", "remove", "upstream")
	r := Repo{Name: "o/n", GitDir: filepath.Join(s.clone, ".git"), WorkDir: s.clone, CloneURL: s.upstream}
	head, _, err := r.FetchPR(ctx, 7, "main")
	if err != nil || head != s.prSHA {
		t.Fatalf("head=%s err=%v", head, err)
	}

	r.CloneURL = ""
	if _, _, err := r.FetchPR(ctx, 7, "main"); err == nil {
		t.Fatal("FetchPR with no remote and no clone URL succeeded")
	}
}

func TestFetchPRRejectsBadBaseRef(t *testing.T) {
	s := newScratch(t)
	r := Repo{Name: "o/n", GitDir: filepath.Join(s.clone, ".git"), WorkDir: s.clone}
	for _, ref := range []string{"", "--upload-pack=x", "a b", "a..b"} {
		if _, _, err := r.FetchPR(context.Background(), 7, ref); err == nil {
			t.Errorf("baseRef %q accepted", ref)
		}
	}
}

func TestGitErrorCarriesStderr(t *testing.T) {
	_, err := Git(context.Background(), t.TempDir(), "rev-parse", "--verify", "nope")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

// userRefs lists every ref except those jawnSnow owns.
func userRefs(t *testing.T, dir string) string {
	t.Helper()
	var keep []string
	for l := range strings.SplitSeq(git(t, dir, "for-each-ref"), "\n") {
		if !strings.Contains(l, "\trefs/jawnsnow/") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func TestBuildEnv(t *testing.T) {
	has := func(env []string, kv string) bool {
		return slices.Contains(env, kv)
	}
	const batch = "GIT_SSH_COMMAND=ssh -o BatchMode=yes"
	env := buildEnv([]string{"A=1"}, false)
	if !has(env, "GIT_TERMINAL_PROMPT=0") || !has(env, "GIT_LFS_SKIP_SMUDGE=1") || !has(env, batch) || !has(env, "A=1") {
		t.Fatalf("plain: %v", env)
	}
	if env := buildEnv([]string{"GIT_SSH_COMMAND=my-ssh"}, false); has(env, batch) {
		t.Fatalf("env ssh command overridden: %v", env)
	}
	if env := buildEnv(nil, true); has(env, batch) || !has(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("core.sshCommand set: %v", env)
	}
}

func TestRemoteForRequiresGitHubHost(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	r := Repo{Name: "o/n", GitDir: filepath.Join(s.clone, ".git"), WorkDir: s.clone}
	git(t, s.clone, "remote", "add", "a-mirror", "https://gitlab.com/o/n.git")
	git(t, s.clone, "remote", "set-url", "upstream", "git@github.com:o/n.git")
	got, err := r.RemoteFor(ctx)
	if err != nil || got != "upstream" {
		t.Fatalf("RemoteFor = %q, %v; want upstream", got, err)
	}
	git(t, s.clone, "remote", "remove", "upstream")
	if got, _ := r.RemoteFor(ctx); got != "" {
		t.Fatalf("gitlab-only: RemoteFor = %q", got)
	}
}

// A cancelled Git asks git to stop with SIGTERM, which git handles by
// removing its lock files. SIGKILL can't be caught, so a checkout killed
// that way leaves index.lock behind.
func TestGitCancelSendsSIGTERM(t *testing.T) {
	bin := t.TempDir()
	started, termed := filepath.Join(bin, "started"), filepath.Join(bin, "termed")
	// The stand-in answers any other call (the ssh-command lookup) at once,
	// and holds a checkout until it is signalled.
	script := `#!/bin/sh
case " $* " in
*" checkout "*) ;;
*) exit 1 ;;
esac
trap 'echo > "` + termed + `"; exit 143' TERM
echo > "` + started + `"
while :; do sleep 0.05; done
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := Git(ctx, t.TempDir(), "checkout", "--detach", "--force", "x"); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in git never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled checkout returned no error")
	}
	if _, err := os.Stat(termed); err != nil {
		t.Errorf("git was not sent SIGTERM: %v", err)
	}
}

// RemoteFor reads URLs from `git remote -v`, so it must see them as
// get-url does: rewritten by insteadOf, and whole when they hold a space.
func TestRemoteForReadsRewrittenAndSpacedURLs(t *testing.T) {
	ctx := context.Background()
	s := newScratch(t)
	r := Repo{Name: "o/n", GitDir: filepath.Join(s.clone, ".git"), WorkDir: s.clone}

	git(t, s.clone, "config", "url.https://github.com/.insteadOf", "gh:")
	git(t, s.clone, "remote", "set-url", "upstream", "gh:o/n")
	if got, err := r.RemoteFor(ctx); err != nil || got != "upstream" {
		t.Errorf("insteadOf: RemoteFor = %q, %v; want upstream", got, err)
	}

	git(t, s.clone, "remote", "set-url", "upstream", "/tmp/a dir/o/n")
	if got, err := r.RemoteFor(ctx); err != nil || got != "upstream" {
		t.Errorf("spaced path: RemoteFor = %q, %v; want upstream", got, err)
	}
}
