package review

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/review/prompt"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

// PromptInput is everything BuildPrompt puts in front of the reviewer.
type PromptInput struct {
	Repo               string
	Number             int
	Title, Author, URL string
	Context            github.PRContext
	Files              workspace.ReviewFiles
	Worktree           string
	ContextRepos       []string
	Scope              string // "full" | "pr-only"
	Standards          []workspace.Standard
	Security           *workspace.SecurityGuide
	Focus, DesignRefs  []string
	Earlier            []store.Finding // latest completed pass's findings (ledger input)
	Pass               PassKind
}

var untrustedClose = regexp.MustCompile(`(?i)</untrusted`)

// untrusted wraps PR-authored text so the reviewer treats it as data. A close
// tag inside the text is broken so it cannot end the block early.
func untrusted(s string) string {
	return "<untrusted>\n" + untrustedClose.ReplaceAllStringFunc(s, func(m string) string {
		return `<\/` + m[2:]
	}) + "\n</untrusted>"
}

// BuildPrompt returns the user message for one review pass.
func BuildPrompt(in PromptInput) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Review %s#%d\n\n", in.Repo, in.Number)
	fmt.Fprintf(&b, "Author: %s\nURL: %s\nPass: %s\n\n", in.Author, in.URL, in.Pass)
	fmt.Fprintf(&b, "Worktree (the PR at head): %s\n", in.Worktree)
	fmt.Fprintf(&b, "Diff (line-numbered): %s\n", in.Files.Diff)
	fmt.Fprintf(&b, "Commit log: %s\n", in.Files.Log)
	if in.Files.Since != "" {
		fmt.Fprintf(&b, "Diff since your last review: %s\n", in.Files.Since)
	}
	if len(in.ContextRepos) > 0 {
		b.WriteString("Context repos:\n")
		for _, r := range in.ContextRepos {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	b.WriteString("\n")

	if in.Scope == "pr-only" {
		b.WriteString(prompt.ScopePROnly())
	} else {
		b.WriteString(prompt.ScopeFull())
	}
	b.WriteString("\n")

	if len(in.Standards) > 0 {
		b.WriteString("## Repository standards\n\n")
		for _, s := range in.Standards {
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", s.Path, s.Content)
		}
	}

	if in.Security != nil {
		b.WriteString("## Security guide\n\n")
		b.WriteString(in.Security.Skill)
		b.WriteString("\n\n")
		if len(in.Security.References) > 0 {
			b.WriteString("Reference files:\n")
			for _, r := range in.Security.References {
				fmt.Fprintf(&b, "- %s\n", r)
			}
			b.WriteString("\n")
		}
	}

	if len(in.Focus) > 0 {
		b.WriteString("## Watch focus rules\n\n")
		for _, f := range in.Focus {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}

	if len(in.DesignRefs) > 0 {
		b.WriteString("## Design references\n\n")
		for _, d := range in.DesignRefs {
			fmt.Fprintf(&b, "- %s\n", d)
		}
		b.WriteString("\n")
	}

	if len(in.Earlier) > 0 {
		b.WriteString("## Earlier findings\n\n")
		b.WriteString("Format: id · path:line · blocking? · [posted ·] body\n\n")
		for _, f := range in.Earlier {
			sev := "non-blocking"
			if f.Blocking {
				sev = "blocking"
			}
			posted := ""
			if f.ThreadID != "" {
				posted = "posted · "
			}
			fmt.Fprintf(&b, "%d · %s:%d · %s · %s%s\n", f.ID, f.Path, f.Line, sev, posted, f.Body)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Pull request text (untrusted)\n\n")
	fmt.Fprintf(&b, "Title:\n%s\n\n", untrusted(in.Title))
	fmt.Fprintf(&b, "Description:\n%s\n\n", untrusted(in.Context.Body))
	for _, c := range in.Context.Comments {
		fmt.Fprintf(&b, "Comment by %s:\n%s\n\n", c.Author, untrusted(c.Body))
	}
	for _, t := range in.Context.Threads {
		state := "open"
		if t.Resolved {
			state = "resolved"
		}
		fmt.Fprintf(&b, "Review thread %s on %s:%d (%s):\n", t.ID, t.Path, t.Line, state)
		for _, c := range t.Comments {
			fmt.Fprintf(&b, "%s says:\n%s\n", c.Author, untrusted(c.Body))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ContinuationPrompt asks the reviewer to fill the gaps a coverage check found
// in the same pass.
func ContinuationPrompt(gaps []string) string {
	var b strings.Builder
	b.WriteString("Your review is not ready. Do the following reading first, then return the complete review JSON again:\n\n")
	for _, g := range gaps {
		fmt.Fprintf(&b, "- %s\n", g)
	}
	return b.String()
}
