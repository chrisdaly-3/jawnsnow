package review

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

const fullOutput = `{
  "findings": [
    {"path": "a.go", "line": 12, "start_line": 10, "blocking": true, "body": "nil deref", "category": "bug"},
    {"path": "b.go", "line": 3, "blocking": false, "body": "nit"}
  ],
  "ledger": [{"id": "42", "status": "addressed"}, {"id": "7", "status": "outstanding"}],
  "design_refs": "docs/design.md\nREADME.md",
  "note": "n",
  "understanding": "u",
  "assumptions": [{"claim": "c", "evidence": "a.go:1"}],
  "verdict": "request_changes",
  "verdict_reason": "r",
  "examined": {"docs": ["README.md"], "code": ["a.go"], "usages": ["c.go"], "tests": ["a_test.go"]}
}`

func TestParseOutput(t *testing.T) {
	got, err := ParseOutput(json.RawMessage(fullOutput))
	if err != nil {
		t.Fatal(err)
	}
	want := Output{
		Findings: []OutFinding{
			{Path: "a.go", Line: 12, StartLine: 10, Blocking: true, Body: "nil deref", Category: "bug"},
			{Path: "b.go", Line: 3, Body: "nit"},
		},
		Ledger:        []LedgerEntry{{ID: 42, State: "addressed"}, {ID: 7, State: "outstanding"}},
		DesignRefs:    []string{"docs/design.md", "README.md"},
		Note:          "n",
		Understanding: "u",
		Assumptions:   []store.Assumption{{Claim: "c", Evidence: "a.go:1"}},
		Verdict:       "request_changes",
		VerdictReason: "r",
		Examined: Examined{
			Docs: []string{"README.md"}, Code: []string{"a.go"},
			Usages: []string{"c.go"}, Tests: []string{"a_test.go"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseOutput =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseOutputErrors(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"nil":          nil,
		"empty":        {},
		"null":         json.RawMessage(`null`),
		"array":        json.RawMessage(`[]`),
		"string":       json.RawMessage(`"x"`),
		"bad ledger":   json.RawMessage(`{"ledger":[{"id":"abc","status":"addressed"}]}`),
		"bad findings": json.RawMessage(`{"findings":"x"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseOutput(raw); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestParseOutputMissingFieldsAreZero(t *testing.T) {
	got, err := ParseOutput(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Output{}) {
		t.Fatalf("got %+v", got)
	}
}
