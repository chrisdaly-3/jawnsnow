// Package workspace finds or clones the repository a PR belongs to, fetches
// the PR's refs from the right remote, and pools the git worktrees Claude
// reviews run in. It touches only what jawnSnow creates: its own worktrees
// and refs under refs/jawnsnow/.
package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

// Repo is a local git repository for a GitHub repo.
type Repo struct {
	Name    string // "owner/name"
	GitDir  string // absolute path of the shared git directory
	WorkDir string // "" for a bare clone
	// CloneURL is where to fetch from when no remote of the repo is the
	// base repo. Resolve sets it.
	CloneURL string
}

// Git runs `git -C dir args...` and returns stdout. A failure carries
// git's stderr. It never prompts for credentials and never uses a shell.
func Git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Resolve returns the repository for name ("owner/name"). A mapped,
// working path wins; otherwise a blobless bare clone lives under
// dataDir/clones and is reused once it exists.
func Resolve(ctx context.Context, name string, rc config.Repo, dataDir string, cloneURL func(name string) string) (Repo, error) {
	if !nameRE.MatchString(name) || strings.Contains(name, "..") {
		return Repo{}, fmt.Errorf("invalid repo name %q", name)
	}
	url := cloneURL(name)

	if rc.Path != "" {
		if r, err := open(ctx, name, expandHome(rc.Path)); err == nil {
			r.CloneURL = url
			return r, nil
		}
	}

	clones := filepath.Join(dataDir, "clones")
	dest := filepath.Join(clones, strings.Replace(name, "/", "__", 1)+".git")
	if r, err := open(ctx, name, dest); err == nil {
		r.CloneURL = url
		return r, nil
	}
	if err := os.MkdirAll(clones, 0o755); err != nil {
		return Repo{}, err
	}
	if _, err := Git(ctx, clones, "clone", "--bare", "--filter=blob:none", "--", url, dest); err != nil {
		return Repo{}, err
	}
	r, err := open(ctx, name, dest)
	if err != nil {
		return Repo{}, err
	}
	r.CloneURL = url
	return r, nil
}

// open inspects path as a repository: it fails if path is not one.
func open(ctx context.Context, name, path string) (Repo, error) {
	out, err := Git(ctx, path, "rev-parse", "--is-bare-repository", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Repo{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return Repo{}, fmt.Errorf("unexpected rev-parse output %q", out)
	}
	r := Repo{Name: name, GitDir: filepath.Clean(lines[1])}
	if lines[0] != "true" {
		top, err := Git(ctx, path, "rev-parse", "--show-toplevel")
		if err != nil {
			return Repo{}, err
		}
		r.WorkDir = strings.TrimSpace(string(top))
	}
	return r, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// dir is where git commands for r run.
func (r Repo) dir() string {
	if r.WorkDir != "" {
		return r.WorkDir
	}
	return r.GitDir
}

// RemoteFor returns the name of the remote whose URL is r's base repo, or
// "" when none is. A fork's origin does not match.
func (r Repo) RemoteFor(ctx context.Context) (string, error) {
	out, err := Git(ctx, r.dir(), "remote")
	if err != nil {
		return "", err
	}
	var match []string
	for _, remote := range strings.Fields(string(out)) {
		u, err := Git(ctx, r.dir(), "remote", "get-url", remote)
		if err != nil {
			continue
		}
		if sameRepo(strings.TrimSpace(string(u)), r.Name) {
			match = append(match, remote)
		}
	}
	for _, m := range match {
		if m == "origin" {
			return m, nil
		}
	}
	if len(match) > 0 {
		return match[0], nil
	}
	return "", nil
}

// sameRepo reports whether url (scp-style, ssh://, https:// or a path)
// names "owner/name", ignoring case, a .git suffix and a trailing slash.
func sameRepo(url, name string) bool {
	u := strings.TrimRight(url, "/")
	u = strings.TrimSuffix(u, ".git")
	parts := strings.FieldsFunc(u, func(c rune) bool { return c == '/' || c == ':' })
	if len(parts) < 2 {
		return false
	}
	return strings.EqualFold(parts[len(parts)-2]+"/"+parts[len(parts)-1], name)
}

// MergeBase returns the best common ancestor of a and b.
func (r Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := Git(ctx, r.dir(), "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
