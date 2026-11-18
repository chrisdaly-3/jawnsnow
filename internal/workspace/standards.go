package workspace

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Standard is one CLAUDE.md or AGENTS.md of the repo at the base commit.
type Standard struct{ Path, Content string }

// SecurityGuide is the repo's security-review skill, read at the base
// commit. Dir is the directory the reference files were written to.
type SecurityGuide struct {
	Dir, Skill string
	References []string // absolute paths of the written files
}

const (
	maxGuideRefs     = 30
	maxGuideFileSize = 64 << 10
)

var (
	securityRoots = []string{"agents/skills", ".claude/skills"}
	refNameRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)
)

// LoadStandards returns every CLAUDE.md and AGENTS.md at base that sits in
// the directory of a changed path or any directory above it, root first,
// then by depth, then by name. It reads base, never a worktree, so a PR
// cannot change the rules it is reviewed against.
func LoadStandards(ctx context.Context, r Repo, base string, changed []string) ([]Standard, error) {
	if !objectID.MatchString(base) {
		return nil, fmt.Errorf("invalid commit ID %q", base)
	}
	seen := map[string]bool{}
	for _, p := range changed {
		dir := path.Dir(p)
		for {
			for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
				seen[path.Join(dir, name)] = true // path.Join drops a "." dir
			}
			if dir == "." || dir == "/" {
				break
			}
			dir = path.Dir(dir)
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	candidates := make([]string, 0, len(seen))
	for p := range seen {
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") {
			candidates = append(candidates, p)
		}
	}
	entries, err := r.lsTree(ctx, base, false, candidates...)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if e.regular() {
			found = append(found, e.path)
		}
	}
	slices.SortFunc(found, func(a, b string) int {
		if c := strings.Count(a, "/") - strings.Count(b, "/"); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	var out []Standard
	for _, p := range found {
		content, err := r.showAt(ctx, base, p)
		if err != nil {
			return nil, err
		}
		out = append(out, Standard{Path: p, Content: string(content)})
	}
	return out, nil
}

// LoadSecurityGuide reads the first skill directory at base under
// agents/skills or .claude/skills whose name contains "security-review".
// Its SKILL.md is returned in Skill and up to 30 of its references/*.md
// files, each at most 64 KiB, are written to outDir/security. A file whose
// name is not a plain Markdown name, or that is too big, is skipped. ok is
// false when base has no such skill.
func LoadSecurityGuide(ctx context.Context, r Repo, base, outDir string) (SecurityGuide, bool, error) {
	if !objectID.MatchString(base) {
		return SecurityGuide{}, false, fmt.Errorf("invalid commit ID %q", base)
	}
	entries, err := r.lsTree(ctx, base, true, securityRoots...)
	if err != nil {
		return SecurityGuide{}, false, err
	}
	var skillDir string
	for _, root := range securityRoots {
		for _, e := range entries { // ls-tree output is sorted by path
			rest, ok := strings.CutPrefix(e.path, root+"/")
			if !ok || !e.regular() {
				continue
			}
			name, file, ok := strings.Cut(rest, "/")
			if ok && file == "SKILL.md" && strings.Contains(name, "security-review") {
				skillDir = root + "/" + name
				break
			}
		}
		if skillDir != "" {
			break
		}
	}
	if skillDir == "" {
		return SecurityGuide{}, false, nil
	}
	skill, err := r.showAt(ctx, base, skillDir+"/SKILL.md")
	if err != nil {
		return SecurityGuide{}, false, err
	}

	abs, err := filepath.Abs(outDir)
	if err != nil {
		return SecurityGuide{}, false, err
	}
	guide := SecurityGuide{Dir: filepath.Join(abs, "security"), Skill: string(skill)}
	if err := os.MkdirAll(guide.Dir, 0o755); err != nil {
		return SecurityGuide{}, false, err
	}
	for _, e := range entries {
		if len(guide.References) >= maxGuideRefs {
			break
		}
		name, ok := strings.CutPrefix(e.path, skillDir+"/references/")
		if !ok || !e.regular() || !refNameRE.MatchString(name) || strings.Contains(name, "..") {
			continue // also skips nested files: a name has no "/"
		}
		content, err := r.showAt(ctx, base, e.path)
		if err != nil {
			return SecurityGuide{}, false, err
		}
		if len(content) > maxGuideFileSize {
			continue
		}
		p := filepath.Join(guide.Dir, name)
		if err := os.WriteFile(p, content, 0o644); err != nil {
			return SecurityGuide{}, false, err
		}
		guide.References = append(guide.References, p)
	}
	return guide, true, nil
}

// treeEntry is one line of `git ls-tree`.
type treeEntry struct{ mode, path string }

// regular reports whether e is a plain file, not a symlink or submodule.
func (e treeEntry) regular() bool { return e.mode == "100644" || e.mode == "100755" }

// lsTree lists the entries of commit under paths, which are matched
// literally; recursive descends into directories. A missing path lists
// nothing.
func (r Repo) lsTree(ctx context.Context, commit string, recursive bool, paths ...string) ([]treeEntry, error) {
	args := slices.Concat(noHooks, []string{"--literal-pathspecs", "ls-tree", "-z"})
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, commit, "--")
	out, err := Git(ctx, r.GitDir, append(args, paths...)...)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for line := range strings.SplitSeq(string(out), "\x00") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		entries = append(entries, treeEntry{mode: strings.Fields(meta)[0], path: p})
	}
	return entries, nil
}

// showAt returns the content of file p at commit.
func (r Repo) showAt(ctx context.Context, commit, p string) ([]byte, error) {
	return Git(ctx, r.GitDir, slices.Concat(noHooks, []string{"show", commit + ":" + p})...)
}
