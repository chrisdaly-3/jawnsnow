package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// OutFinding is one finding of the reviewer's output.
type OutFinding struct {
	Path            string
	Line, StartLine int
	Blocking        bool
	Body, Category  string
}

// LedgerEntry is the reviewer's status for one earlier finding.
type LedgerEntry struct {
	ID    int64
	State string // addressed, outstanding or answered
}

// Examined lists what the reviewer says it opened, by kind.
type Examined struct{ Docs, Code, Usages, Tests []string }

// Output is the reviewer's structured output, shaped by prompt/schema.json.
type Output struct {
	Findings []OutFinding
	Ledger   []LedgerEntry
	// BadLedgerIDs are ledger ids that aren't integers, such as a GitHub
	// review thread ID. They name no earlier finding and are skipped.
	BadLedgerIDs           []string
	DesignRefs             []string
	Note, Understanding    string
	Assumptions            []store.Assumption
	Verdict, VerdictReason string
	Examined               Examined
}

type rawOutput struct {
	Findings []struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		StartLine int    `json:"start_line"`
		Blocking  bool   `json:"blocking"`
		Body      string `json:"body"`
		Category  string `json:"category"`
	} `json:"findings"`
	Ledger []struct {
		ID     json.RawMessage `json:"id"`
		Status string          `json:"status"`
	} `json:"ledger"`
	DesignRefs  json.RawMessage `json:"design_refs"`
	Note        string          `json:"note"`
	Understand  string          `json:"understanding"`
	Assumptions []struct {
		Claim    string `json:"claim"`
		Evidence string `json:"evidence"`
	} `json:"assumptions"`
	Verdict       string `json:"verdict"`
	VerdictReason string `json:"verdict_reason"`
	Examined      struct {
		Docs   []string `json:"docs"`
		Code   []string `json:"code"`
		Usages []string `json:"usages"`
		Tests  []string `json:"tests"`
	} `json:"examined"`
}

// ParseOutput reads the reviewer's output. It fails on empty output and on
// JSON that is not an object. A ledger id that is not an integer, written as
// a number or a string, goes to BadLedgerIDs instead: one stray entry must
// not throw away a finished review.
// Fields the output omits stay zero; the schema, enforced by claude, is
// what requires them.
func ParseOutput(raw json.RawMessage) (Output, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Output{}, errors.New("review output is empty")
	}
	if raw[0] != '{' {
		return Output{}, errors.New("review output is not a JSON object")
	}
	var r rawOutput
	if err := json.Unmarshal(raw, &r); err != nil {
		return Output{}, fmt.Errorf("parse review output: %w", err)
	}
	out := Output{
		Note:          r.Note,
		Understanding: r.Understand,
		Verdict:       r.Verdict,
		VerdictReason: r.VerdictReason,
		Examined:      Examined(r.Examined),
	}
	for _, f := range r.Findings {
		out.Findings = append(out.Findings, OutFinding{
			Path: f.Path, Line: f.Line, StartLine: f.StartLine,
			Blocking: f.Blocking, Body: f.Body, Category: f.Category,
		})
	}
	for _, l := range r.Ledger {
		text := string(bytes.TrimSpace(l.ID))
		var s string
		if json.Unmarshal(l.ID, &s) == nil {
			text = s
		}
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			out.BadLedgerIDs = append(out.BadLedgerIDs, text)
			continue
		}
		out.Ledger = append(out.Ledger, LedgerEntry{ID: id, State: l.Status})
	}
	for _, a := range r.Assumptions {
		out.Assumptions = append(out.Assumptions, store.Assumption{Claim: a.Claim, Evidence: a.Evidence})
	}
	refs, err := parseDesignRefs(r.DesignRefs)
	if err != nil {
		return Output{}, err
	}
	out.DesignRefs = refs
	return out, nil
}

// parseDesignRefs reads design_refs, a string in the schema with one
// reference per line. An array of strings is accepted too.
func parseDesignRefs(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var lines []string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		lines = strings.Split(s, "\n")
	} else if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, errors.New("design_refs is neither a string nor a list of strings")
	}
	var refs []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			refs = append(refs, l)
		}
	}
	return refs, nil
}
