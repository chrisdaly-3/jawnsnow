// Package post turns a stored review and the user's selections into the exact
// GitHub mutation input and a preview of it. It is pure: nothing here talks to
// GitHub, so what the preview shows is decided in one place.
package post

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

type Event string

const (
	Approve        Event = "APPROVE"
	Comment        Event = "COMMENT"
	RequestChanges Event = "REQUEST_CHANGES"
)

// Body is the user's own summary; Build appends the checked unanchored
// findings to it. Include is keyed by Finding.ID; a missing entry means included.
type Selection struct {
	Event   Event
	Body    string
	Include map[int64]bool
}

type Part struct{ Label, Markdown string }

type Preview struct {
	Event  Event
	Commit string   // short OID
	Parts  []Part   // the summary first (when non-blank), then each thread in payload order
	Hidden []string // HiddenContent over every part, each prefixed "<label>: "
	// ThreadFindings is the finding ID behind each of in.Threads, in the
	// same order, so a posted comment can be traced back to its finding.
	ThreadFindings []int64
}

const shortOIDLen = 7

// DefaultSelection leaves findings that were already dealt with unchecked, so
// a re-review does not post the same point twice. The body stays empty: it is
// the user's own summary, and Build adds the checked unanchored findings to it.
// Claude's understanding and verdict reason stay private.
func DefaultSelection(r store.Review) Selection {
	sel := Selection{Event: eventFor(r.Verdict), Include: map[int64]bool{}}
	for _, f := range r.Findings {
		if f.Ledger == "addressed" || f.Ledger == "answered" || f.ThreadID != "" {
			sel.Include[f.ID] = false
		}
	}
	return sel
}

func eventFor(verdict string) Event {
	switch verdict {
	case "approve":
		return Approve
	case "request_changes":
		return RequestChanges
	}
	return Comment
}

// otherNotes renders unanchored findings as one markdown list. Later lines of
// a body are indented two spaces so they stay inside their list item.
func otherNotes(fs []store.Finding) string {
	items := make([]string, len(fs))
	for i, f := range fs {
		body := strings.ReplaceAll(Sanitize(f.Body), "\n", "\n  ")
		items[i] = "- " + codeSpan(Sanitize(f.Path)) + ": " + body
	}
	return "**Other notes**\n\n" + strings.Join(items, "\n")
}

// codeSpan wraps s in a backtick run one longer than any run inside it, padded
// with spaces, so a path cannot close its own span early.
func codeSpan(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	if longest == 0 {
		return "`" + s + "`"
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + s + " " + fence
}

func Build(r store.Review, prID string, sel Selection) (in github.PostInput, p Preview, err error) {
	switch sel.Event {
	case Approve, Comment, RequestChanges:
	default:
		return in, p, fmt.Errorf("unknown review event %q", sel.Event)
	}

	var threads []github.PostThread
	var threadFindings []int64
	var notes []store.Finding
	for _, f := range r.Findings {
		if inc, ok := sel.Include[f.ID]; ok && !inc {
			continue
		}
		if f.Unanchored {
			notes = append(notes, f)
			continue
		}
		if f.Line < 1 {
			return in, p, fmt.Errorf("finding in %s has no line to anchor to", f.Path)
		}
		t := github.PostThread{Path: Sanitize(f.Path), Line: f.Line, Body: Sanitize(f.Body)}
		if f.StartLine > 0 && f.StartLine < f.Line {
			t.StartLine = f.StartLine
		}
		threads = append(threads, t)
		threadFindings = append(threadFindings, f.ID)
	}

	own := Sanitize(sel.Body)
	ownBlank := strings.TrimSpace(own) == ""
	body := own
	if len(notes) > 0 {
		if ownBlank {
			body = otherNotes(notes)
		} else {
			body = strings.TrimLeft(own, "\n") + "\n\n" + otherNotes(notes)
		}
	} else if ownBlank {
		body = ""
	}

	in = github.PostInput{PRID: prID, CommitOID: r.HeadOID, Event: string(sel.Event), Body: body, Threads: threads}
	p = Preview{Event: sel.Event, Commit: shortOID(r.HeadOID), ThreadFindings: threadFindings}
	add := func(label, md string) {
		p.Parts = append(p.Parts, Part{Label: label, Markdown: md})
		for _, h := range HiddenContent(md) {
			p.Hidden = append(p.Hidden, label+": "+h)
		}
	}
	if body != "" {
		add("Summary", in.Body)
	}
	for _, t := range in.Threads {
		label := fmt.Sprintf("%s:%d", t.Path, t.Line)
		if t.StartLine > 0 {
			label = fmt.Sprintf("%s:%d-%d", t.Path, t.StartLine, t.Line)
		}
		add(label, t.Body)
	}

	// A selection that can't post yet still gets its preview, so the
	// findings it would post show while the summary is being written; the
	// payload comes back empty, so nothing can be sent until it can.
	switch {
	case sel.Event == RequestChanges && ownBlank:
		return github.PostInput{}, p, errors.New("Request changes needs a summary — write one in the box")
	case sel.Event == Comment && body == "" && len(threads) == 0:
		return github.PostInput{}, p, errors.New("nothing to post: write a summary or select a finding")
	}
	return in, p, nil
}

func shortOID(oid string) string {
	if len(oid) > shortOIDLen {
		return oid[:shortOIDLen]
	}
	return oid
}

// OSC runs to BEL or ST. A bare ESC that starts neither sequence is dropped
// with the other control characters, so no sequence survives half-stripped.
var (
	oscSeq = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	csiSeq = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]?`)
)

// Sanitize strips what could drive a terminal or hide text: control
// characters other than newline and tab, and ANSI CSI/OSC sequences whole.
// A character reference that decodes to one of those becomes U+FFFD; that
// runs last, so stripping can't join the pieces of a new one.
func Sanitize(s string) string {
	s = oscSeq.ReplaceAllString(s, "")
	s = csiSeq.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if stripped(r) {
			return -1
		}
		return r
	}, s)
	return ReplaceControlRefs(s)
}

// invisible reports zero-width and bidi format characters, which can hide or
// reorder text without being visible in a preview.
func invisible(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E,
		r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069, r == 0xFEFF:
		return true
	}
	return false
}
