package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file removes things jawnSnow created: refs under refs/jawnsnow/,
// review files under the data directory's reviews/, and worktrees under a
// pool's slots directory. It never runs `git worktree prune` or `git gc`:
// prune is repo-wide and would drop the user's own worktrees whose
// directories are missing.

// ReleasePR deletes refs/jawnsnow/<number>/*, removes reviewDirs (each must
// be an absolute path inside the data directory's reviews/), and frees the
// slot held for number. An invalid directory is skipped and reported after
// everything else has been done.
func (p *Pool) ReleasePR(ctx context.Context, number int, reviewDirs []string) error {
	if err := validateSlotsDir(p.dir); err != nil {
		return err
	}
	var errs []error
	if err := deleteRefs(ctx, p.repo, func(n int) bool { return n == number }); err != nil {
		errs = append(errs, err)
	}

	// The pool directory is <data>/worktrees/<owner>__<name>.
	reviews := filepath.Join(filepath.Dir(filepath.Dir(p.dir)), "reviews")
	for _, d := range reviewDirs {
		if !filepath.IsAbs(d) || !strictlyInside(reviews, d) {
			errs = append(errs, fmt.Errorf("refusing to remove %q: not inside %s", d, reviews))
			continue
		}
		if err := os.RemoveAll(d); err != nil {
			errs = append(errs, err)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	slots, err := p.st.Slots(ctx, p.repo.Name)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, s := range slots {
		if filepath.Dir(s.Path) != p.dir || s.PRNumber != number {
			continue
		}
		delete(p.held, s.Path)
		s.Busy, s.PRNumber = false, 0
		if err := p.st.SaveSlot(ctx, s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PruneIdle removes the worktrees of free slots unused for longer than idle.
func (p *Pool) PruneIdle(ctx context.Context, idle time.Duration) error {
	if err := validateSlotsDir(p.dir); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	slots, err := p.st.Slots(ctx, p.repo.Name)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range slots {
		if filepath.Dir(s.Path) != p.dir || s.Busy || p.now().Sub(s.LastUsed) <= idle {
			continue
		}
		if _, held := p.held[s.Path]; held {
			continue
		}
		if err := removeWorktree(ctx, p.repo, s.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := p.st.DeleteSlot(ctx, s.Path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sweep clears what an earlier run left behind. No review survives a
// restart, so every stored slot becomes free. It removes worktrees and
// directories only under slotsDir, and refs only under refs/jawnsnow/<N>/
// for an N not in tracked.
func Sweep(ctx context.Context, r Repo, slotsDir string, tracked map[int]bool, st SlotStore) error {
	if err := validateSlotsDir(slotsDir); err != nil {
		return err
	}
	slotsDir = filepath.Clean(slotsDir)
	var errs []error
	failed := map[string]bool{} // directories whose worktree git refused to remove

	slots, err := st.Slots(ctx, r.Name)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, s := range slots {
		if filepath.Dir(s.Path) != slotsDir {
			continue
		}
		if _, err := os.Lstat(s.Path); errors.Is(err, os.ErrNotExist) {
			if err := removeWorktree(ctx, r, s.Path); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := st.DeleteSlot(ctx, s.Path); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		known[s.Path] = true
		if s.Busy {
			s.Busy = false
			if err := st.SaveSlot(ctx, s); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// Worktrees git lists under slotsDir that no stored slot owns.
	listed, err := worktreePaths(ctx, r)
	if err != nil {
		errs = append(errs, err)
	}
	for _, path := range listed {
		if !strictlyInside(slotsDir, path) || !samePath(filepath.Dir(path), slotsDir) {
			continue
		}
		owned := false
		for k := range known {
			owned = owned || samePath(k, path)
		}
		if !owned {
			if err := removeWorktree(ctx, r, path); err != nil {
				// Deleting the directory anyway would leave a stale
				// registration behind.
				failed[resolve(path)] = true
				errs = append(errs, err)
			}
		}
	}

	// Plain directories (or symlinks, which are unlinked, not followed).
	entries, err := os.ReadDir(slotsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, e := range entries {
		path := filepath.Join(slotsDir, e.Name())
		if known[path] || failed[resolve(path)] || !strictlyInside(slotsDir, path) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, err)
		}
	}

	if err := deleteRefs(ctx, r, func(n int) bool { return !tracked[n] }); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateSlotsDir refuses a slots directory that could make cleanup
// delete something that is not a jawnSnow worktree pool: it must be absolute,
// not the root or the home directory, and sit directly under a directory
// named "worktrees" (<data>/worktrees/<owner>__<name>).
func validateSlotsDir(dir string) error {
	c := filepath.Clean(dir)
	if !filepath.IsAbs(dir) || c == string(filepath.Separator) || filepath.Base(filepath.Dir(c)) != "worktrees" {
		return fmt.Errorf("unsafe slots directory %q", dir)
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(c, home) {
		return fmt.Errorf("unsafe slots directory %q", dir)
	}
	return nil
}

// deleteRefs deletes refs/jawnsnow/<N>/* for every N that match accepts.
func deleteRefs(ctx context.Context, r Repo, match func(n int) bool) error {
	out, err := Git(ctx, r.dir(), "for-each-ref", "--format=%(refname)", "refs/jawnsnow/")
	if err != nil {
		return err
	}
	var errs []error
	for ref := range strings.SplitSeq(string(out), "\n") {
		rest, ok := strings.CutPrefix(ref, "refs/jawnsnow/")
		if !ok {
			continue
		}
		num, _, ok := strings.Cut(rest, "/")
		n, convErr := strconv.Atoi(num)
		// The trailing slash match means N=7 never selects refs/jawnsnow/70/.
		if !ok || convErr != nil || strconv.Itoa(n) != num || !match(n) {
			continue
		}
		if _, err := Git(ctx, r.dir(), "update-ref", "-d", ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeWorktree unregisters and deletes the worktree at path. It succeeds
// when git no longer lists path, including when it never did.
func removeWorktree(ctx context.Context, r Repo, path string) error {
	_, err := Git(ctx, r.dir(), "worktree", "remove", "--force", path)
	if err == nil {
		return nil
	}
	listed, lerr := worktreePaths(ctx, r)
	if lerr != nil {
		return err
	}
	for _, l := range listed {
		if samePath(l, path) {
			return err
		}
	}
	return nil
}

// worktreePaths lists every worktree path git knows for r.
func worktreePaths(ctx context.Context, r Repo) ([]string, error) {
	out, err := Git(ctx, r.dir(), "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func samePath(a, b string) bool {
	return resolve(a) == resolve(b)
}

// resolve cleans p and resolves symlinks in its existing parent, so
// /var/... and /private/var/... compare equal.
func resolve(p string) string {
	p = filepath.Clean(p)
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(dir, filepath.Base(p))
	}
	return p
}

// strictlyInside reports whether path lies below root, by path
// arithmetic only: no ".." and not root itself. The final element is never
// resolved, so a symlink there counts as its own path, not its target.
func strictlyInside(root, path string) bool {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	rel, err := filepath.Rel(resolve(root), resolve(path))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
