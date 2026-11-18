package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The git helpers below are trimmed copies of internal/workspace's test
// helpers, which other packages can't import.

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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scratch is an upstream bare repo "o/n" whose main holds README.md and
// a.txt, and whose refs/pull/7/head is one commit on top that adds b.txt.
type scratch struct {
	root, upstream, work string
	mainSHA, prSHA       string
}

func newScratch(t *testing.T) scratch {
	t.Helper()
	root := realPath(t, t.TempDir())
	s := scratch{root: root, upstream: filepath.Join(root, "o", "n.git"), work: filepath.Join(root, "work")}
	for _, d := range []string{s.work, filepath.Join(root, "o")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, s.work, "init")
	writeFile(t, filepath.Join(s.work, "README.md"), "# n\n")
	writeFile(t, filepath.Join(s.work, "a.txt"), "one\n")
	git(t, s.work, "add", "README.md", "a.txt")
	git(t, s.work, "commit", "-m", "base")
	s.mainSHA = git(t, s.work, "rev-parse", "HEAD")
	git(t, root, "clone", "--bare", s.work, s.upstream)

	git(t, s.work, "checkout", "-b", "pr")
	s.prSHA = s.addPRCommit(t, "b.txt")
	return s
}

// addPRCommit adds file on top of the PR branch, points upstream's
// refs/pull/7/head at the new commit and returns its ID.
func (s scratch) addPRCommit(t *testing.T, file string) string {
	t.Helper()
	writeFile(t, filepath.Join(s.work, file), file+"\n")
	git(t, s.work, "add", file)
	git(t, s.work, "commit", "-m", "pr "+file)
	sha := git(t, s.work, "rev-parse", "HEAD")
	git(t, s.work, "push", s.upstream, "HEAD:refs/heads/tmp")
	git(t, s.upstream, "update-ref", "refs/pull/7/head", sha)
	git(t, s.upstream, "update-ref", "-d", "refs/heads/tmp")
	return sha
}
