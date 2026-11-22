package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// ClonePath is where Resolve keeps its blobless bare clone of repo
// ("owner/name") under the data directory.
func ClonePath(dataDir, repo string) string {
	return filepath.Join(dataDir, "clones", repoDirName(repo)+".git")
}

// PoolDir is the directory that holds repo's worktree slots under the data
// directory.
func PoolDir(dataDir, repo string) string {
	return filepath.Join(dataDir, "worktrees", repoDirName(repo))
}

// repoDirName is repo as one path element: owner__name.
func repoDirName(repo string) string { return strings.ReplaceAll(repo, "/", "__") }

// ExpandHome replaces a leading ~ in a configured path with the user's home
// directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
