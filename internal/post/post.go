// Package post turns a stored review and the user's selections into the exact
// GitHub mutation input and a preview of it. It is pure: nothing here talks to
// GitHub, so what the preview shows is decided in one place.
package post

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

type Event string

const (
	Approve        Event = "APPROVE"
	Comment        Event = "COMMENT"
	RequestChanges Event = "REQUEST_CHANGES"
)

// Include is keyed by Finding.ID; a missing entry means included.
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
}

const shortOIDLen = 7

// DefaultSelection leaves findings that were already dealt with unchecked, so
// a re-review does not post the same point twice. The body carries only the
// unanchored findings; Claude's understanding and verdict reason stay private.
func DefaultSelection(r store.Review) Selection {
	sel := Selection{Event: eventFor(r.Verdict), Include: map[int64]bool{}}
	var notes []store.Finding
	for _, f := range r.Findings {
		if f.Ledger == "addressed" || f.Ledger == "answered" || f.ThreadID != "" {
			sel.Include[f.ID] = false
			continue
		}
		if f.Unanchored {
			notes = append(notes, f)
		}
	}
	sel.Body = strings.TrimLeft(otherNotes(notes), "\n")
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
	if len(fs) == 0 {
		return ""
	}
	return "\n\n**Other notes**\n\n" + noteItems(fs)
}

func noteItems(fs []store.Finding) string {
	items := make([]string, len(fs))
	for i, f := range fs {
		body := strings.ReplaceAll(Sanitize(f.Body), "\n", "\n  ")
		items[i] = fmt.Sprintf("- `%s`: %s", Sanitize(f.Path), body)
	}
	return strings.Join(items, "\n")
}

func Build(r store.Review, prID string, sel Selection) (in github.PostInput, p Preview, err error) {
	switch sel.Event {
	case Approve, Comment, RequestChanges:
	default:
		return in, p, fmt.Errorf("unknown review event %q", sel.Event)
	}

	var threads []github.PostThread
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
	}

	body := Sanitize(sel.Body)
	if len(notes) > 0 && !strings.Contains(body, noteItems(notes)) {
		body += otherNotes(notes)
	}
	body = strings.TrimLeft(body, "\n")
	if strings.TrimSpace(body) == "" {
		body = ""
	}

	if body == "" {
		switch {
		case sel.Event == RequestChanges:
			return in, p, errors.New("Request changes needs a summary — press e to write one")
		case sel.Event == Comment && len(threads) == 0:
			return in, p, errors.New("nothing to post: write a summary or select a finding")
		}
	}

	in = github.PostInput{PRID: prID, CommitOID: r.HeadOID, Event: string(sel.Event), Body: body, Threads: threads}
	p = Preview{Event: sel.Event, Commit: shortOID(r.HeadOID)}
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
func Sanitize(s string) string {
	s = oscSeq.ReplaceAllString(s, "")
	s = csiSeq.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
