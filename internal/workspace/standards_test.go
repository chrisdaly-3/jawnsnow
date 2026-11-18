package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandardsFromBaseNotHead(t *testing.T) {
	h := newHistory(t)
	base := h.commit("base", map[string][]byte{"CLAUDE.md": []byte("base rules\n"), "a.go": []byte("x\n")})
	h.commit("pr", map[string][]byte{"CLAUDE.md": []byte("pr rewrote the rules\n"), "a.go": []byte("y\n")})

	got, err := LoadStandards(context.Background(), h.repo(), base, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "CLAUDE.md" || got[0].Content != "base rules\n" {
		t.Fatalf("got %+v", got)
	}
}

func TestStandardsWalkUpAndDedupe(t *testing.T) {
	h := newHistory(t)
	base := h.commit("base", map[string][]byte{
		"CLAUDE.md":         []byte("root claude"),
		"AGENTS.md":         []byte("root agents"),
		"a/CLAUDE.md":       []byte("a claude"),
		"a/b/AGENTS.md":     []byte("ab agents"),
		"a/b/CLAUDE.md":     []byte("ab claude"),
		"a/b/c.go":          []byte("x"),
		"a/d.go":            []byte("x"),
		"other/CLAUDE.md":   []byte("other"),
		"a/b/sub/CLAUDE.md": []byte("below a changed dir, not on a path"),
	})
	got, err := LoadStandards(context.Background(), h.repo(), base, []string{"a/b/c.go", "a/d.go", "new.go"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, s := range got {
		paths = append(paths, s.Path)
	}
	want := "AGENTS.md CLAUDE.md a/CLAUDE.md a/b/AGENTS.md a/b/CLAUDE.md"
	if strings.Join(paths, " ") != want {
		t.Fatalf("paths = %v, want %s", paths, want)
	}
	if got[4].Content != "ab claude" {
		t.Fatalf("content = %q", got[4].Content)
	}
}

func TestStandardsRejectsBadBase(t *testing.T) {
	h := newHistory(t)
	h.commit("base", map[string][]byte{"a.go": []byte("x")})
	if _, err := LoadStandards(context.Background(), h.repo(), "--output=x", []string{"a.go"}); err == nil {
		t.Fatal("want an error for a non-hex base")
	}
	if _, _, err := LoadSecurityGuide(context.Background(), h.repo(), "main", t.TempDir()); err == nil {
		t.Fatal("want an error for a non-hex base")
	}
}

func TestSecurityGuideLimits(t *testing.T) {
	h := newHistory(t)
	dir := "agents/skills/my-security-review/"
	files := map[string][]byte{
		dir + "SKILL.md":                     []byte("the skill"),
		dir + "references/.hidden.md":        []byte("bad name"),
		dir + "references/bad name.md":       []byte("bad name"),
		dir + "references/notes.txt":         []byte("not md"),
		dir + "references/big.md":            []byte(strings.Repeat("x", 70<<10)),
		dir + "references/nested/deep.md":    []byte("nested"),
		"agents/skills/other-skill/SKILL.md": []byte("not this one"),
	}
	for i := range 31 {
		files[fmt.Sprintf("%sreferences/ref-%02d.md", dir, i)] = []byte("ref")
	}
	base := h.commit("base", files)

	out := t.TempDir()
	g, ok, err := LoadSecurityGuide(context.Background(), h.repo(), base, out)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if g.Skill != "the skill" {
		t.Fatalf("skill = %q", g.Skill)
	}
	if g.Dir != filepath.Join(out, "security") {
		t.Fatalf("dir = %q", g.Dir)
	}
	if len(g.References) != 30 {
		t.Fatalf("got %d references: %v", len(g.References), g.References)
	}
	for _, p := range g.References {
		if filepath.Dir(p) != g.Dir || !strings.HasPrefix(filepath.Base(p), "ref-") {
			t.Fatalf("unexpected reference %q", p)
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != "ref" {
			t.Fatalf("%s: %q %v", p, b, err)
		}
	}
	entries, _ := os.ReadDir(g.Dir)
	if len(entries) != 30 {
		t.Fatalf("%d files written, want 30", len(entries))
	}
}

func TestSecurityGuideFromBaseAndClaudeRoot(t *testing.T) {
	h := newHistory(t)
	base := h.commit("base", map[string][]byte{".claude/skills/security-review/SKILL.md": []byte("base skill")})
	h.commit("pr", map[string][]byte{".claude/skills/security-review/SKILL.md": []byte("pr skill")})
	g, ok, err := LoadSecurityGuide(context.Background(), h.repo(), base, t.TempDir())
	if err != nil || !ok || g.Skill != "base skill" {
		t.Fatalf("g=%+v ok=%v err=%v", g, ok, err)
	}
}

func TestNoSecurityGuide(t *testing.T) {
	h := newHistory(t)
	base := h.commit("base", map[string][]byte{"agents/skills/lint/SKILL.md": []byte("x")})
	_, ok, err := LoadSecurityGuide(context.Background(), h.repo(), base, t.TempDir())
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
