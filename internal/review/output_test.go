package review

import (
	"encoding/json"
	"reflect"
	"slices"
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
		"bad findings": json.RawMessage(`{"findings":"x"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseOutput(raw); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// TestParseOutputSkipsLedgerIDsThatAreNotFindings uses the ledger shape that
// failed both reviews of chainguard-dev/mono#85931: the reviewer listed the
// PR's GitHub review threads by thread ID. A ledger entry that can't name an
// earlier finding is skipped and reported, never a reason to drop the review.
func TestParseOutputSkipsLedgerIDsThatAreNotFindings(t *testing.T) {
	raw := json.RawMessage(`{"findings":[],"verdict":"approve","ledger":[
		{"id":"PRRT_kwDOGIaHbc65lRmx","status":"addressed"},
		{"id":"12","status":"outstanding"},
		{"id":7,"status":"addressed"},
		{"id":"abc","status":"addressed"}]}`)
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	if want := []LedgerEntry{{ID: 12, State: "outstanding"}, {ID: 7, State: "addressed"}}; !slices.Equal(out.Ledger, want) {
		t.Errorf("Ledger = %v, want %v", out.Ledger, want)
	}
	if want := []string{"PRRT_kwDOGIaHbc65lRmx", "abc"}; !slices.Equal(out.BadLedgerIDs, want) {
		t.Errorf("BadLedgerIDs = %v, want %v", out.BadLedgerIDs, want)
	}
	if out.Verdict != "approve" {
		t.Errorf("Verdict = %q, want approve", out.Verdict)
	}
}

func TestLedgerNote(t *testing.T) {
	earlier := []store.Finding{{ID: 7}, {ID: 9}}
	cases := []struct {
		name string
		out  Output
		want string
	}{
		{"all match", Output{Ledger: []LedgerEntry{{ID: 7}, {ID: 9}}}, ""},
		{"none given", Output{}, ""},
		{"one unknown id", Output{Ledger: []LedgerEntry{{ID: 7}, {ID: 12}}}, "ignored 1 ledger entry that matches no earlier finding"},
		{"thread ids and an unknown id", Output{Ledger: []LedgerEntry{{ID: 12}}, BadLedgerIDs: []string{"PRRT_a", "PRRT_b"}}, "ignored 3 ledger entries that match no earlier finding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ledgerNote(earlier, tc.out); got != tc.want {
				t.Errorf("ledgerNote = %q, want %q", got, tc.want)
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
