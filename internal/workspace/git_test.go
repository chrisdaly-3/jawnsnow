package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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

	// A mapped path that is not a repo falls through to the clone.
	r, err := Resolve(ctx, "o/n", config.Repo{Path: filepath.Join(s.root, "missing")}, dataDir, url)
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
	branchesBefore := git(t, s.clone, "for-each-ref", "refs/heads")

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
	if got := git(t, s.clone, "for-each-ref", "refs/heads"); got != branchesBefore {
		t.Fatalf("user branches changed:\n%s\n->\n%s", branchesBefore, got)
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
