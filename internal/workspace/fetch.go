package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// fetchLocks serializes fetches into one git dir, process-wide: two
// concurrent fetches into the same repo contend for its ref locks.
var fetchLocks sync.Map // cleaned absolute git dir -> *sync.Mutex

func fetchLock(gitDir string) *sync.Mutex {
	key := gitDir
	if abs, err := filepath.Abs(gitDir); err == nil {
		key = abs
	}
	m, _ := fetchLocks.LoadOrStore(filepath.Clean(key), &sync.Mutex{})
	return m.(*sync.Mutex)
}

// FetchPR fetches PR number's head and its base branch into
// refs/jawnsnow/<number>/{head,base} and returns both commit IDs. It reads
// from the remote that is the base repo, else from r.CloneURL. It writes
// only under refs/jawnsnow/.
func (r Repo) FetchPR(ctx context.Context, number int, baseRef string) (head, base string, err error) {
	if baseRef == "" || strings.HasPrefix(baseRef, "-") {
		return "", "", fmt.Errorf("invalid base ref %q", baseRef)
	}
	if _, err := Git(ctx, r.dir(), "check-ref-format", "refs/heads/"+baseRef); err != nil {
		return "", "", fmt.Errorf("invalid base ref %q", baseRef)
	}
	source, err := r.RemoteFor(ctx)
	if err != nil {
		return "", "", err
	}
	if source == "" {
		source = r.CloneURL
	}
	if source == "" {
		return "", "", errors.New("no remote is the base repo and no clone URL is known")
	}

	headRef := fmt.Sprintf("refs/jawnsnow/%d/head", number)
	baseLocal := fmt.Sprintf("refs/jawnsnow/%d/base", number)

	mu := fetchLock(r.GitDir)
	mu.Lock()
	defer mu.Unlock()
	// gc.auto and maintenance are off so a fetch never repacks or prunes a
	// user's clone.
	// --refmap= stops git also updating refs/remotes/<remote>/* from the
	// remote's configured fetch refspec.
	_, err = Git(ctx, r.dir(),
		"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.hooksPath=/dev/null",
		"fetch", "--refmap=", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--", source,
		fmt.Sprintf("+refs/pull/%d/head:%s", number, headRef),
		fmt.Sprintf("+refs/heads/%s:%s", baseRef, baseLocal),
	)
	if err != nil {
		return "", "", err
	}
	if head, err = revParse(ctx, r, headRef); err != nil {
		return "", "", err
	}
	if base, err = revParse(ctx, r, baseLocal); err != nil {
		return "", "", err
	}
	return head, base, nil
}

func revParse(ctx context.Context, r Repo, ref string) (string, error) {
	out, err := Git(ctx, r.dir(), "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
