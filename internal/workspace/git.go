// Package workspace finds or clones the repository a PR belongs to, fetches
// the PR's refs from the right remote, and pools the git worktrees Claude
// reviews run in. It touches only what jawnSnow creates: its own worktrees
// and refs under refs/jawnsnow/.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

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
// git's stderr. It never prompts for credentials, never runs hooks (a
// relative core.hooksPath would resolve inside a worktree, which holds the
// PR's files), never recurses into submodules or smudges LFS files (a PR's
// .gitmodules or .lfsconfig would make a checkout fetch from hosts it
// names) and never uses a shell. Every call gets this, not just fetch.
func Git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = buildEnv(os.Environ(), sshCommandConfigured(ctx, dir))
	// A cancel asks git to stop with SIGTERM, which it handles by removing
	// its lock files. The default, SIGKILL, can't be caught: a checkout
	// killed that way leaves index.lock behind.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// A git that ignores SIGTERM is killed after WaitDelay, and a killed git
	// can leave a child (ssh, git-remote-https) holding the pipes; WaitDelay
	// stops Run waiting on it forever.
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// buildEnv returns environ with prompts and LFS smudging disabled. When no
// ssh command is configured anywhere, ssh is told never to prompt, so it
// cannot grab /dev/tty.
func buildEnv(environ []string, sshCommandConfigured bool) []string {
	env := append(slices.Clip(environ), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	if sshCommandConfigured {
		return env
	}
	for _, e := range environ {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			return env
		}
	}
	return append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
}

// sshProbe caches the core.sshCommand probe per directory for the life of
// the process, so the extra `git config` runs once per directory rather
// than once per git call. A config change while snow runs is not seen.
var sshProbe sync.Map // dir -> bool

// sshCommandConfigured reports whether core.sshCommand is set for dir.
func sshCommandConfigured(ctx context.Context, dir string) bool {
	if v, ok := sshProbe.Load(dir); ok {
		return v.(bool)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "core.sshCommand").Output()
	set := err == nil && len(strings.TrimSpace(string(out))) > 0
	// Exit 1 means "not set"; any other failure (missing dir, cancelled
	// context) says nothing, so it is not cached.
	var exit *exec.ExitError
	if err == nil || errors.As(err, &exit) && exit.ExitCode() == 1 {
		sshProbe.Store(dir, set)
	}
	return set
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Resolve returns the repository for name ("owner/name"): the mapped path
// when there is one, which must be a repository, or else a blobless bare
// clone under dataDir/clones, reused once it exists. A mapped path that is
// not a repository is an error and never cloned in its place, since it is
// most likely a typo.
func Resolve(ctx context.Context, name string, rc config.Repo, dataDir string, cloneURL func(name string) string) (Repo, error) {
	if !nameRE.MatchString(name) || strings.Contains(name, "..") {
		return Repo{}, fmt.Errorf("invalid repo name %q", name)
	}
	url := cloneURL(name)

	if rc.Path != "" {
		r, err := open(ctx, name, expandHome(rc.Path))
		if err != nil {
			if ctx.Err() != nil {
				return Repo{}, ctx.Err()
			}
			return Repo{}, fmt.Errorf("repos.%q.path %s is not a git repository", name, rc.Path)
		}
		r.CloneURL = url
		return r, nil
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
	for remote := range strings.FieldsSeq(string(out)) {
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
	if h := urlHost(u); h != "" && !strings.EqualFold(h, "github.com") {
		return false
	}
	parts := strings.FieldsFunc(u, func(c rune) bool { return c == '/' || c == ':' })
	if len(parts) < 2 {
		return false
	}
	return strings.EqualFold(parts[len(parts)-2]+"/"+parts[len(parts)-1], name)
}

// urlHost returns the host of an scp-style or scheme URL, or "" for a
// local path.
func urlHost(u string) string {
	if _, after, ok := strings.Cut(u, "://"); ok {
		host := after
		if j := strings.IndexByte(host, '/'); j >= 0 {
			host = host[:j]
		}
		if k := strings.LastIndexByte(host, '@'); k >= 0 {
			host = host[k+1:]
		}
		if c := strings.IndexByte(host, ':'); c >= 0 {
			host = host[:c]
		}
		return host
	}
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") {
		return ""
	}
	if c := strings.IndexByte(u, ':'); c >= 0 && !strings.Contains(u[:c], "/") {
		host := u[:c]
		if k := strings.LastIndexByte(host, '@'); k >= 0 {
			host = host[k+1:]
		}
		return host
	}
	return ""
}

// MergeBase returns the best common ancestor of a and b.
func (r Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := Git(ctx, r.dir(), "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
