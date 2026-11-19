package review

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/review/prompt"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// recordCall saves one fake claude invocation as call-<n>.json in dir and
// returns the fixture for it, <n>.jsonl in the same dir. Calls are
// numbered from 1 in the order they arrive; the engine runs them one at a
// time.
func recordCall(dir, stdin string) string {
	prior, _ := filepath.Glob(filepath.Join(dir, "call-*.json"))
	n := len(prior) + 1
	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	data, _ := json.Marshal(fakeCall{Args: args, Stdin: stdin})
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("call-%d.json", n)), data, 0o600)
	return filepath.Join(dir, fmt.Sprintf("%d.jsonl", n))
}

// fillPlaceholders replaces {{REVIEW_DIR}} with the first --add-dir and
// {{WORKTREE}} with the working directory, so a fixture can name the
// absolute paths the engine chose.
func fillPlaceholders(data []byte) []byte {
	if i := slices.Index(os.Args, "--add-dir"); i >= 0 && i+1 < len(os.Args) {
		data = bytes.ReplaceAll(data, []byte("{{REVIEW_DIR}}"), []byte(os.Args[i+1]))
	}
	if wd, err := os.Getwd(); err == nil {
		data = bytes.ReplaceAll(data, []byte("{{WORKTREE}}"), []byte(wd))
	}
	return data
}

type fakeCall struct {
	Args  []string
	Stdin string
}

// flag returns the value after name in the call's args, or "".
func (c fakeCall) flag(name string) string {
	if i := slices.Index(c.Args, name); i >= 0 && i+1 < len(c.Args) {
		return c.Args[i+1]
	}
	return ""
}

// fakeRun is what one fake claude invocation reports.
type fakeRun struct {
	Reads, Searched []string
	Output          any     // structured_output; nil leaves it out
	Cost            float64 // this run's own cost
	IsError         bool
}

type fakeClaude struct {
	t   *testing.T
	dir string
}

func newFakeClaude(t *testing.T) fakeClaude {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("GO_CALL_DIR", dir)
	return fakeClaude{t: t, dir: dir}
}

// script writes the stream for each run, in order. The runs are one
// session, so each reports total_cost_usd as the running total over the
// runs so far, as real claude does on --resume.
func (f fakeClaude) script(runs ...fakeRun) {
	f.t.Helper()
	var total float64
	for i, r := range runs {
		total += r.Cost
		var b bytes.Buffer
		line := func(v any) {
			data, err := json.Marshal(v)
			if err != nil {
				f.t.Fatal(err)
			}
			b.Write(append(data, '\n'))
		}
		tool := func(name, key, path string) map[string]any {
			return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "name": name, "input": map[string]any{key: path}},
			}}}
		}
		line(map[string]any{"type": "system", "subtype": "init", "session_id": "s"})
		for _, p := range r.Reads {
			line(tool("Read", "file_path", p))
		}
		for _, p := range r.Searched {
			line(tool("Grep", "path", p))
		}
		subtype := "success"
		if r.IsError {
			subtype = "error_max_turns"
		}
		res := map[string]any{
			"type": "result", "subtype": subtype, "is_error": r.IsError, "session_id": "s",
			"total_cost_usd": total, "num_turns": 2, "duration_ms": 1500,
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 7},
		}
		if r.Output != nil {
			res["structured_output"] = r.Output
		}
		line(res)
		if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("%d.jsonl", i+1)), b.Bytes(), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f fakeClaude) calls() []fakeCall {
	f.t.Helper()
	var out []fakeCall
	for n := 1; ; n++ {
		data, err := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("call-%d.json", n)))
		if errors.Is(err, os.ErrNotExist) {
			return out
		}
		if err != nil {
			f.t.Fatal(err)
		}
		var c fakeCall
		if err := json.Unmarshal(data, &c); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, c)
	}
}

// readAll is the reading a clean pass over the scratch PR does: the diff
// and the README that documents the changed files, as absolute paths the
// way claude reports them.
var readAll = []string{"{{REVIEW_DIR}}/diff.txt", "{{WORKTREE}}/README.md"}

type outFinding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Blocking bool   `json:"blocking"`
	Body     string `json:"body"`
	Category string `json:"category"`
}

type ledgerItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func output(findings []outFinding, ledger []ledgerItem) map[string]any {
	if findings == nil {
		findings = []outFinding{}
	}
	if ledger == nil {
		ledger = []ledgerItem{}
	}
	return map[string]any{
		"findings": findings, "ledger": ledger, "design_refs": "", "note": "one note",
		"understanding": "adds b.txt", "assumptions": []any{},
		"verdict": "comment", "verdict_reason": "one question",
		"examined": map[string][]string{
			"docs": {"README.md"}, "code": {}, "usages": {}, "tests": {"never_read_test.go"},
		},
	}
}

var bFinding = outFinding{Path: "b.txt", Line: 1, Body: "b.txt should say two.", Category: "bug"}

// fakeGitHub serves ReviewContext; the engine calls nothing else.
type fakeGitHub struct {
	github.Client
	pc github.PRContext
}

func (f fakeGitHub) ReviewContext(ctx context.Context, _ string) (github.PRContext, github.RateLimit, error) {
	if err := ctx.Err(); err != nil {
		return github.PRContext{}, github.RateLimit{}, err
	}
	return f.pc, github.RateLimit{}, nil
}

type engineEnv struct {
	s      scratch
	e      *Engine
	st     *store.Store
	data   string
	claude fakeClaude
	dbPath string
	mu     sync.Mutex
	now    time.Time
	cfg    config.Config
	snap   github.Snapshot
}

func newEngineEnv(t *testing.T) *engineEnv {
	t.Helper()
	env := &engineEnv{s: newScratch(t), claude: newFakeClaude(t), cfg: config.Defaults()}
	env.data = filepath.Join(realPath(t, t.TempDir()), "jawnsnow")
	env.dbPath = filepath.Join(env.data, "jawnsnow.db")
	st, err := store.Open(env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	env.st = st
	env.now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	env.e = &Engine{
		Config:   func() config.Config { return env.cfg },
		Client:   fakeGitHub{pc: github.PRContext{Body: "PR body here", Files: []string{"b.txt"}}},
		Store:    st,
		DataDir:  env.data,
		Claude:   os.Args[0],
		Viewer:   func() string { return "me" },
		Now:      env.clock,
		CloneURL: func(string) string { return env.s.upstream },
		binArgs:  []string{"-test.run=TestHelperProcess", "--"},
	}
	env.snap = github.Snapshot{
		ID: "PR_7", Repo: "o/n", Number: 7, Title: "Add b", Author: "sam",
		URL: "https://github.com/o/n/pull/7", State: github.PROpen,
		HeadOID: env.s.prSHA, BaseRef: "main", Additions: 1,
		Commits: []github.Commit{{OID: env.s.prSHA, Parents: []string{env.s.mainSHA}}},
	}
	return env
}

func (env *engineEnv) clock() time.Time {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.now = env.now.Add(time.Second)
	return env.now
}

func (env *engineEnv) advance(d time.Duration) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.now = env.now.Add(d)
}

func (env *engineEnv) run(t *testing.T, req Request) (store.Review, error) {
	t.Helper()
	req.PR = tracker.Tracked{PR: env.snap}
	return env.e.Run(context.Background(), req, nil)
}

func (env *engineEnv) costRows(t *testing.T) []store.CostRow {
	t.Helper()
	db, err := sql.Open("sqlite", env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM cost_ledger ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []store.CostRow
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		var c store.CostRow
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func (env *engineEnv) stored(t *testing.T) []store.Review {
	t.Helper()
	rs, err := env.st.Reviews(context.Background(), env.snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// savePrior stores a completed review of the PR at head with two open
// findings on b.txt and returns their IDs.
func (env *engineEnv) savePrior(t *testing.T, head string) (int64, int64) {
	t.Helper()
	prior := store.Review{
		ID: "prior", PRID: env.snap.ID, Repo: "o/n", Number: 7, Author: "sam",
		HeadOID: head, BaseOID: env.s.mainSHA, MergeBaseOID: env.s.mainSHA,
		Tier: "first_pass", PassKind: "full", Mode: "auto", Outcome: "complete",
		StartedAt: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		Findings: []store.Finding{
			{Path: "b.txt", Line: 1, Body: "first", Ledger: "new"},
			{Path: "b.txt", Line: 1, Body: "second", Ledger: "outstanding"},
		},
	}
	if err := env.st.SaveReview(context.Background(), prior); err != nil {
		t.Fatal(err)
	}
	rs := env.stored(t)
	return rs[0].Findings[0].ID, rs[0].Findings[1].ID
}

// pushSecondCommit adds c.txt on top of the PR and makes it the head.
func (env *engineEnv) pushSecondCommit(t *testing.T) string {
	t.Helper()
	b := env.s.addPRCommit(t, "c.txt")
	env.snap.HeadOID = b
	env.snap.Commits = append(env.snap.Commits, github.Commit{OID: b, Parents: []string{env.s.prSHA}})
	return b
}

func (env *engineEnv) reviewDir(id string) string { return filepath.Join(env.data, "reviews", id) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestEngineFullPassSaved(t *testing.T) {
	env := newEngineEnv(t)
	env.claude.script(fakeRun{Reads: readAll, Output: output([]outFinding{bFinding}, nil), Cost: 0.5})

	var events []Event
	rv, err := env.e.Run(context.Background(), Request{PR: tracker.Tracked{PR: env.snap}, Mode: ModeManual},
		func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rv.Outcome != "complete" || rv.PassKind != "full" || rv.Tier != "first_pass" || rv.Mode != "manual" {
		t.Errorf("outcome/pass/tier/mode = %s/%s/%s/%s", rv.Outcome, rv.PassKind, rv.Tier, rv.Mode)
	}
	if rv.Model != "claude-opus-5-5" || rv.Effort != "high" {
		t.Errorf("model/effort = %s/%s", rv.Model, rv.Effort)
	}
	if rv.HeadOID != env.s.prSHA || rv.BaseOID != env.s.mainSHA || rv.MergeBaseOID != env.s.mainSHA {
		t.Errorf("head/base/merge-base = %s/%s/%s", rv.HeadOID, rv.BaseOID, rv.MergeBaseOID)
	}
	if rv.CostUSD != 0.5 || rv.SessionID == "" || rv.Verdict != "comment" || rv.Error != "" {
		t.Errorf("review = %+v", rv)
	}
	if !strings.Contains(rv.Note, "one note") {
		t.Errorf("note = %q", rv.Note)
	}

	saved := env.stored(t)
	if len(saved) != 1 || saved[0].ID != rv.ID || len(saved[0].Findings) != 1 {
		t.Fatalf("stored = %+v", saved)
	}
	f := saved[0].Findings[0]
	if f.Path != "b.txt" || f.Line != 1 || f.Ledger != "new" || f.Unanchored {
		t.Errorf("finding = %+v", f)
	}
	var raw struct{ Examined map[string][]string }
	if err := json.Unmarshal(saved[0].Raw, &raw); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if !slices.Equal(raw.Examined["docs"], []string{"README.md"}) || len(raw.Examined["tests"]) != 0 {
		t.Errorf("raw examined = %v, want only what was read", raw.Examined)
	}

	rows := env.costRows(t)
	if len(rows) != 1 {
		t.Fatalf("cost rows = %d, want 1", len(rows))
	}
	want := store.CostRow{
		ReviewID: rv.ID, PRID: "PR_7", Repo: "o/n", Author: "sam", Tier: "first_pass", PassKind: "full",
		Mode: "manual", Model: "claude-opus-5-5", Effort: "high", Outcome: "complete",
		CostUSD: 0.5, TokensIn: 10, TokensOut: 5, TokensCached: 7, Duration: 1500 * time.Millisecond,
	}
	got := rows[0]
	got.At = time.Time{}
	if got != want {
		t.Errorf("cost row:\n got %+v\nwant %+v", got, want)
	}

	slots, err := env.st.Slots(context.Background(), "o/n")
	if err != nil || len(slots) != 1 || slots[0].Busy {
		t.Errorf("slots = %+v, %v; want one free slot", slots, err)
	}
	if !exists(filepath.Join(env.reviewDir(rv.ID), "diff.txt")) || !exists(filepath.Join(env.reviewDir(rv.ID), "log.txt")) {
		t.Errorf("review dir %s lacks diff.txt or log.txt", env.reviewDir(rv.ID))
	}

	calls := env.claude.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.flag("--session-id") != rv.SessionID || c.flag("--add-dir") != env.reviewDir(rv.ID) ||
		c.flag("--tools") != "Read,Grep,Glob" || c.flag("--max-turns") != "100" ||
		c.flag("--max-budget-usd") != "15" || c.flag("--json-schema") != prompt.Schema() {
		t.Errorf("args = %q", c.Args)
	}
	// The empty --setting-sources value must reach claude as its own argument.
	if i := slices.Index(c.Args, "--setting-sources"); i < 0 || i+1 >= len(c.Args) || c.Args[i+1] != "" ||
		!slices.Contains(c.Args, "--strict-mcp-config") {
		t.Errorf("claude did not get --setting-sources \"\" and --strict-mcp-config: %q", c.Args)
	}
	if !strings.HasPrefix(c.Stdin, prompt.Reviewer()) || !strings.Contains(c.Stdin, "PR body here") ||
		strings.Contains(c.Stdin, "Diff since your last review") {
		t.Errorf("prompt does not start with reviewer.md, lacks the PR body, or has since.txt:\n%s", c.Stdin)
	}
	if len(events) != 2 || events[0].Tool != "Read" {
		t.Errorf("events = %v", events)
	}
}

func TestEngineContinuationFillsGaps(t *testing.T) {
	env := newEngineEnv(t)
	env.claude.script(
		fakeRun{Reads: []string{"{{WORKTREE}}/README.md"}, Output: output(nil, nil), Cost: 0.5},
		fakeRun{Reads: []string{"{{REVIEW_DIR}}/diff.txt"}, Output: output(nil, nil), Cost: 0.25},
	)
	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	calls := env.claude.calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	id := calls[0].flag("--session-id")
	if id == "" || calls[1].flag("--resume") != id || calls[1].flag("--session-id") != "" || rv.SessionID != id {
		t.Errorf("session: first %q, second resume %q, review %q", id, calls[1].flag("--resume"), rv.SessionID)
	}
	if !strings.Contains(calls[1].Stdin, "read the diff file") {
		t.Errorf("continuation prompt = %q", calls[1].Stdin)
	}
	if calls[1].flag("--max-budget-usd") != "14.5" {
		t.Errorf("continuation budget = %s, want what is left of 15", calls[1].flag("--max-budget-usd"))
	}
	rows := env.costRows(t)
	if len(rows) != 2 || rows[0].Outcome != "incomplete" || rows[1].Outcome != "complete" ||
		rows[0].CostUSD != 0.5 || rows[1].CostUSD != 0.25 {
		t.Errorf("cost rows = %+v; want each run's own cost", rows)
	}
	if rv.CostUSD != 0.75 || rv.Outcome != "complete" {
		t.Errorf("cost %v outcome %s", rv.CostUSD, rv.Outcome)
	}
}

func TestEngineIncompleteAfterTwoContinuations(t *testing.T) {
	env := newEngineEnv(t)
	gappy := fakeRun{Reads: []string{"{{WORKTREE}}/README.md"}, Output: output(nil, nil), Cost: 0.25}
	env.claude.script(gappy, gappy, gappy, gappy)
	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(env.claude.calls()); n != 3 {
		t.Errorf("calls = %d, want 3", n)
	}
	if rv.Outcome != "incomplete" || rv.CostUSD != 0.75 || len(env.costRows(t)) != 3 {
		t.Errorf("outcome %s cost %v rows %d", rv.Outcome, rv.CostUSD, len(env.costRows(t)))
	}
	if !strings.Contains(rv.Note, "read the diff file") {
		t.Errorf("note %q does not name the gap", rv.Note)
	}
	if env.stored(t)[0].Outcome != "incomplete" {
		t.Errorf("stored outcome = %s", env.stored(t)[0].Outcome)
	}
}

func TestEngineLintAsksForRewording(t *testing.T) {
	env := newEngineEnv(t)
	banned := outFinding{Path: "b.txt", Line: 1, Body: "This is notably wrong.", Category: "bug"}
	run := fakeRun{Reads: readAll, Output: output([]outFinding{banned}, nil), Cost: 0.1}
	env.claude.script(run, run, run)
	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	calls := env.claude.calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	if !strings.Contains(calls[1].Stdin, `reword: finding 1: uses "notably"`) {
		t.Errorf("continuation prompt = %q", calls[1].Stdin)
	}
	if rv.Outcome != "complete" {
		t.Errorf("outcome = %s; lint alone must not make a review incomplete", rv.Outcome)
	}
	if !strings.Contains(rv.Note, `finding 1: uses "notably"`) {
		t.Errorf("note %q lacks the lint line", rv.Note)
	}
}

func TestEngineIncrementalPass(t *testing.T) {
	env := newEngineEnv(t)
	first, second := env.savePrior(t, env.s.prSHA)
	env.pushSecondCommit(t)
	cFinding := outFinding{Path: "c.txt", Line: 1, Body: "c.txt is new.", Category: "bug"}
	env.claude.script(fakeRun{Reads: readAll, Output: output([]outFinding{cFinding},
		[]ledgerItem{{ID: fmt.Sprint(first), Status: "addressed"}}), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if rv.PassKind != "incremental" {
		t.Errorf("pass = %s", rv.PassKind)
	}
	if !exists(filepath.Join(env.reviewDir(rv.ID), "since.txt")) {
		t.Error("since.txt not written")
	}
	stdin := env.claude.calls()[0].Stdin
	if !strings.Contains(stdin, "Diff since your last review: "+filepath.Join(env.reviewDir(rv.ID), "since.txt")) {
		t.Errorf("prompt lacks since.txt:\n%s", stdin)
	}
	if !strings.Contains(stdin, fmt.Sprintf("%d · b.txt:1 · non-blocking · first", first)) {
		t.Errorf("prompt lacks the earlier finding:\n%s", stdin)
	}
	saved := env.stored(t)[0]
	if saved.ID != rv.ID || len(saved.Findings) != 3 {
		t.Fatalf("saved = %+v", saved)
	}
	got := map[string]string{}
	for _, f := range saved.Findings {
		got[f.Body] = f.Ledger
	}
	want := map[string]string{"c.txt is new.": "new", "first": "addressed", "second": "outstanding"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ledger = %v, want %v (second id %d)", got, want, second)
	}
}

func TestEngineIncrementalWithMissingCommitsIsFull(t *testing.T) {
	env := newEngineEnv(t)
	gone := strings.Repeat("1", 40) // in Commits, but never in the repository
	env.savePrior(t, gone)
	b := env.pushSecondCommit(t)
	env.snap.Commits = []github.Commit{{OID: gone, Parents: []string{env.s.mainSHA}}, {OID: b, Parents: []string{gone}}}
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if rv.PassKind != "full" || exists(filepath.Join(env.reviewDir(rv.ID), "since.txt")) {
		t.Errorf("pass = %s; want full with no since.txt", rv.PassKind)
	}
	if stdin := env.claude.calls()[0].Stdin; !strings.Contains(stdin, "Earlier findings") {
		t.Error("earlier findings left out of the ledger")
	}
	if len(env.stored(t)[0].Findings) != 2 {
		t.Errorf("findings = %+v, want the 2 earlier ones", env.stored(t)[0].Findings)
	}
}

func TestEngineForceIsFull(t *testing.T) {
	env := newEngineEnv(t)
	env.savePrior(t, env.s.prSHA)
	env.pushSecondCommit(t)
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeManual, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if rv.PassKind != "full" || exists(filepath.Join(env.reviewDir(rv.ID), "since.txt")) {
		t.Errorf("pass = %s; want full with no since.txt", rv.PassKind)
	}
	if stdin := env.claude.calls()[0].Stdin; strings.Contains(stdin, "Diff since your last review") || !strings.Contains(stdin, "Earlier findings") {
		t.Errorf("prompt:\n%s", stdin)
	}
}

func TestEngineForcePushIsFull(t *testing.T) {
	env := newEngineEnv(t)
	env.savePrior(t, strings.Repeat("2", 40)) // a head the PR no longer has
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if rv.PassKind != "full" {
		t.Errorf("pass = %s", rv.PassKind)
	}
}

// A push after the poll that queued the review moves refs/pull/N/head past
// the snapshot's head; the review is of the head the quiet period covered.
func TestEngineReviewsSnapshotHeadAfterLaterPush(t *testing.T) {
	env := newEngineEnv(t)
	later := env.s.addPRCommit(t, "c.txt") // pushed after the poll; the snapshot head stays prSHA
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if rv.HeadOID != env.s.prSHA || rv.HeadOID == later {
		t.Errorf("reviewed head %s, want the snapshot head %s", rv.HeadOID, env.s.prSHA)
	}
	diff, err := os.ReadFile(filepath.Join(env.reviewDir(rv.ID), "diff.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diff), "c.txt") {
		t.Errorf("the diff has the later push's c.txt:\n%s", diff)
	}
}

// A force push leaves the snapshot's head out of the fetch: the review stops
// before it costs anything, and nothing is saved.
func TestEngineHeadMovedStopsBeforeClaude(t *testing.T) {
	env := newEngineEnv(t)
	env.snap.HeadOID = strings.Repeat("3", 40) // a head the fetch didn't bring
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	_, err := env.run(t, Request{Mode: ModeAuto})
	if !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err = %v, want ErrHeadMoved", err)
	}
	if n := len(env.claude.calls()); n != 0 {
		t.Errorf("claude ran %d times", n)
	}
	if n := len(env.costRows(t)); n != 0 {
		t.Errorf("cost rows = %d", n)
	}
	if saved := env.stored(t); len(saved) != 0 {
		t.Errorf("stored = %+v, want no review", saved)
	}
	if slots, _ := env.st.Slots(context.Background(), "o/n"); len(slots) != 0 {
		t.Errorf("slots = %+v, want none acquired", slots)
	}
}

func TestEngineFetchFailureIsFailed(t *testing.T) {
	env := newEngineEnv(t)
	env.snap.Number = 99 // upstream has no refs/pull/99/head
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err == nil {
		t.Fatal("Run succeeded")
	}
	if rv.Outcome != "failed" || !strings.HasPrefix(rv.Error, "fetch PR: ") || !strings.Contains(rv.Error, "couldn't find remote ref") {
		t.Errorf("outcome %s error %q", rv.Outcome, rv.Error)
	}
	if n := len(env.claude.calls()); n != 0 {
		t.Errorf("claude ran %d times", n)
	}
	if n := len(env.costRows(t)); n != 0 {
		t.Errorf("cost rows = %d", n)
	}
	if saved := env.stored(t); len(saved) != 1 || saved[0].Outcome != "failed" || saved[0].Error != rv.Error {
		t.Errorf("stored = %+v", saved)
	}
}

func TestEngineRunErrorIsFailed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		run     fakeRun
		wantErr string
	}{
		{"error result", fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 1.5, IsError: true}, "error result"},
		{"no output", fakeRun{Reads: readAll, Cost: 1.5}, "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEngineEnv(t)
			env.claude.script(tc.run, tc.run)
			rv, err := env.run(t, Request{Mode: ModeAuto})
			if err == nil || rv.Outcome != "failed" || !strings.Contains(rv.Error, tc.wantErr) {
				t.Errorf("err %v outcome %s error %q", err, rv.Outcome, rv.Error)
			}
			if n := len(env.claude.calls()); n != 1 {
				t.Errorf("calls = %d, want 1", n)
			}
			rows := env.costRows(t)
			if len(rows) != 1 || rows[0].CostUSD != 1.5 || rows[0].Outcome != "failed" || rv.CostUSD != 1.5 {
				t.Errorf("cost rows = %+v, review cost %v", rows, rv.CostUSD)
			}
			if slots, _ := env.st.Slots(context.Background(), "o/n"); len(slots) != 1 || slots[0].Busy {
				t.Errorf("slots = %+v", slots)
			}
		})
	}
}

// A run killed before its result line has spent an unknown amount; the
// ledger says so rather than recording $0.
func TestEngineKilledRunCostIsUnknown(t *testing.T) {
	env := newEngineEnv(t)
	env.cfg.Review.RunTimeout.Duration = 300 * time.Millisecond
	t.Setenv("GO_WAIT_FILE", filepath.Join(t.TempDir(), "never"))
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err == nil || rv.Outcome != "failed" {
		t.Fatalf("err %v outcome %s, want a failed review", err, rv.Outcome)
	}
	if !rv.CostUnknown || rv.CostUSD != 0 {
		t.Errorf("review CostUnknown %v cost %v", rv.CostUnknown, rv.CostUSD)
	}
	rows := env.costRows(t)
	if len(rows) != 1 || rows[0].Outcome != "failed" || !rows[0].CostUnknown || rows[0].CostUSD != 0 {
		t.Errorf("cost rows = %+v, want one failed row with cost unknown", rows)
	}
	if saved := env.stored(t); len(saved) != 1 || !saved[0].CostUnknown {
		t.Errorf("stored = %+v, want CostUnknown kept", saved)
	}
}

func TestEngineCancelledContextFails(t *testing.T) {
	env := newEngineEnv(t)
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rv, err := env.e.Run(ctx, Request{PR: tracker.Tracked{PR: env.snap}, Mode: ModeAuto}, nil)
	if !errors.Is(err, context.Canceled) || rv.Outcome != "failed" {
		t.Errorf("err %v outcome %s", err, rv.Outcome)
	}
	if n := len(env.claude.calls()); n != 0 {
		t.Errorf("claude ran %d times", n)
	}
}

func TestEngineFollowUpTier(t *testing.T) {
	env := newEngineEnv(t)
	env.snap.Reviews = []github.Review{{Author: "me", State: "APPROVED", SubmittedAt: time.Now()}}
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})

	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	c := env.claude.calls()[0]
	if c.flag("--model") != "claude-sonnet-5-5" || c.flag("--effort") != "medium" || rv.Tier != "follow_up" {
		t.Errorf("model %s effort %s tier %s", c.flag("--model"), c.flag("--effort"), rv.Tier)
	}
}

func TestEngineReleasePR(t *testing.T) {
	env := newEngineEnv(t)
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})
	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(env.data, "clones", "o__n.git")
	if refs := git(t, clone, "for-each-ref", "refs/jawnsnow/"); refs == "" {
		t.Fatal("no refs/jawnsnow refs after a review")
	}
	if err := env.e.ReleasePR(context.Background(), "o/n", 7); err != nil {
		t.Fatalf("ReleasePR: %v", err)
	}
	if exists(env.reviewDir(rv.ID)) {
		t.Error("review dir kept")
	}
	if refs := git(t, clone, "for-each-ref", "refs/jawnsnow/"); refs != "" {
		t.Errorf("refs kept: %s", refs)
	}
	if slots, _ := env.st.Slots(context.Background(), "o/n"); len(slots) != 1 || slots[0].PRNumber != 0 {
		t.Errorf("slots = %+v", slots)
	}
}

func TestEngineReleaseUnreviewedPRClonesNothing(t *testing.T) {
	env := newEngineEnv(t)
	if err := env.e.ReleasePR(context.Background(), "o/n", 8); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(env.data, "clones")) {
		t.Error("ReleasePR cloned a repo to release a PR that was never reviewed")
	}
}

func TestEnginePoolOnePerRepo(t *testing.T) {
	env := newEngineEnv(t)
	var wg sync.WaitGroup
	pools := make([]any, 8)
	errs := make([]error, 8)
	for i := range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := env.e.Pool(context.Background(), "o/n")
			pools[i], errs[i] = p, err
		}()
	}
	wg.Wait()
	for i := range pools {
		if errs[i] != nil {
			t.Fatalf("Pool %d: %v", i, errs[i])
		}
		if pools[i] != pools[0] || pools[i] == nil {
			t.Errorf("Pool %d differs", i)
		}
	}
}

func TestEnginePruneIdle(t *testing.T) {
	env := newEngineEnv(t)
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})
	if _, err := env.run(t, Request{Mode: ModeAuto}); err != nil {
		t.Fatal(err)
	}
	if err := env.e.PruneIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slots, _ := env.st.Slots(context.Background(), "o/n"); len(slots) != 1 {
		t.Fatalf("a fresh slot was pruned: %+v", slots)
	}
	env.advance(3 * time.Hour)
	if err := env.e.PruneIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slots, _ := env.st.Slots(context.Background(), "o/n"); len(slots) != 0 {
		t.Errorf("idle slot kept: %+v", slots)
	}
}

// A slot left by an earlier snow is pruned even when this one never
// reviewed its repo.
func TestEnginePruneIdleCoversReposNotReviewedThisSession(t *testing.T) {
	env := newEngineEnv(t)
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})
	if _, err := env.run(t, Request{Mode: ModeAuto}); err != nil {
		t.Fatal(err)
	}
	slots, _ := env.st.Slots(context.Background(), "o/n")
	if len(slots) != 1 {
		t.Fatalf("slots = %+v", slots)
	}
	restarted := &Engine{
		Config: env.e.Config, Client: env.e.Client, Store: env.st, DataDir: env.data,
		Claude: env.e.Claude, Now: env.clock, CloneURL: env.e.CloneURL, binArgs: env.e.binArgs,
	}
	if err := restarted.PruneIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.st.Slots(context.Background(), "o/n"); len(got) != 1 {
		t.Fatalf("a fresh slot was pruned: %+v", got)
	}
	env.advance(3 * time.Hour)
	if err := restarted.PruneIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.st.Slots(context.Background(), "o/n"); len(got) != 0 {
		t.Errorf("idle slot of a repo not reviewed this session kept: %+v", got)
	}
	if exists(slots[0].Path) {
		t.Errorf("worktree %s kept", slots[0].Path)
	}
}

// A repo whose stored slots can't be resolved is skipped, with its error.
func TestEnginePruneIdleReportsUnresolvableRepo(t *testing.T) {
	env := newEngineEnv(t)
	slot := store.Slot{Repo: "o/gone", Path: filepath.Join(env.data, "worktrees", "o__gone", "slot-1"), LastUsed: env.now.Add(-5 * time.Hour)}
	if err := env.st.SaveSlot(context.Background(), slot); err != nil {
		t.Fatal(err)
	}
	env.cfg.Repos = map[string]config.Repo{"o/gone": {Path: t.TempDir(), Scope: "full"}}
	err := env.e.PruneIdle(context.Background())
	if err == nil || !strings.Contains(err.Error(), "o/gone") {
		t.Errorf("PruneIdle err = %v, want one naming o/gone", err)
	}
}

func TestEngineContinuationFailureKeepsEarlierOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		cont fakeRun
	}{
		{"error result", fakeRun{Cost: 0.25, IsError: true, Output: output(nil, nil)}},
		{"no output", fakeRun{Cost: 0.25}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEngineEnv(t)
			gappy := fakeRun{Reads: []string{"{{WORKTREE}}/README.md"}, Output: output([]outFinding{bFinding}, nil), Cost: 0.5}
			env.claude.script(gappy, tc.cont, gappy)
			rv, err := env.run(t, Request{Mode: ModeAuto})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if n := len(env.claude.calls()); n != 2 {
				t.Errorf("calls = %d, want 2 (no run after the failed continuation)", n)
			}
			if rv.Outcome != "incomplete" || rv.Error != "" || rv.Verdict != "comment" {
				t.Errorf("outcome %s error %q verdict %q", rv.Outcome, rv.Error, rv.Verdict)
			}
			if !strings.Contains(rv.Note, "continuation 1 failed: ") || !strings.Contains(rv.Note, "showing the earlier pass") {
				t.Errorf("note = %q", rv.Note)
			}
			saved := env.stored(t)
			if len(saved) != 1 || saved[0].Outcome != "incomplete" || len(saved[0].Findings) != 1 || saved[0].Findings[0].Path != "b.txt" {
				t.Errorf("stored = %+v", saved)
			}
			rows := env.costRows(t)
			if len(rows) != 2 || rows[0].Outcome != "incomplete" || rows[1].Outcome != "failed" ||
				rows[0].CostUSD != 0.5 || rows[1].CostUSD != 0.25 || rv.CostUSD != 0.75 {
				t.Errorf("cost rows = %+v, review cost %v", rows, rv.CostUSD)
			}
		})
	}
}

func TestEngineStopsWhenBudgetUsed(t *testing.T) {
	env := newEngineEnv(t)
	env.cfg.Review.Budget.PerReviewUSD = 0.5
	gappy := fakeRun{Reads: []string{"{{WORKTREE}}/README.md"}, Output: output(nil, nil), Cost: 0.5}
	env.claude.script(gappy, gappy)
	rv, err := env.run(t, Request{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(env.claude.calls()); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
	if rv.Outcome != "incomplete" || !strings.Contains(rv.Note, "stopped: review budget used") {
		t.Errorf("outcome %s note %q", rv.Outcome, rv.Note)
	}
}

func TestEngineReleasePRWaitsForRun(t *testing.T) {
	env := newEngineEnv(t)
	gate := filepath.Join(t.TempDir(), "go")
	t.Setenv("GO_WAIT_FILE", gate)
	env.claude.script(fakeRun{Reads: readAll, Output: output(nil, nil), Cost: 0.3})
	// A ReleasePR that got in between the review and its save would miss
	// the new review's ID.
	env.e.beforeSave = func() { time.Sleep(300 * time.Millisecond) }

	type result struct {
		rv  store.Review
		err error
	}
	runDone := make(chan result, 1)
	go func() {
		rv, err := env.e.Run(context.Background(), Request{PR: tracker.Tracked{PR: env.snap}, Mode: ModeAuto}, nil)
		runDone <- result{rv, err}
	}()
	for deadline := time.Now().Add(10 * time.Second); len(env.claude.calls()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("claude never started")
		}
	}
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- env.e.ReleasePR(context.Background(), "o/n", 7) }()
	select {
	case err := <-releaseDone:
		t.Fatalf("ReleasePR returned while the review ran: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	writeFile(t, gate, "")

	r := <-runDone
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("ReleasePR: %v", err)
	}
	if exists(env.reviewDir(r.rv.ID)) {
		t.Error("the new review's dir survived ReleasePR")
	}
	if saved := env.stored(t); len(saved) != 1 || saved[0].ID != r.rv.ID || saved[0].Outcome != "complete" {
		t.Errorf("stored = %+v", saved)
	}
}

func TestLedgerStates(t *testing.T) {
	earlier := []store.Finding{
		{ID: 1, Body: "posted", ThreadID: "T1", Ledger: "outstanding"},
		{ID: 2, Body: "unposted", Ledger: "new"},
	}
	got := applyLedger(earlier, []LedgerEntry{{ID: 1, State: "answered"}, {ID: 2, State: "answered"}})
	if got[0].Ledger != "answered" {
		t.Errorf("posted answered = %s, want answered", got[0].Ledger)
	}
	if got[1].Ledger != "outstanding" {
		t.Errorf("unposted answered = %s, want outstanding", got[1].Ledger)
	}

	open := openFindings([]store.Finding{
		{Body: "a", Ledger: "addressed"}, {Body: "b", Ledger: "answered"},
		{Body: "c", Ledger: "new"}, {Body: "d", Ledger: "outstanding"},
	})
	var bodies []string
	for _, f := range open {
		bodies = append(bodies, f.Body)
	}
	if !slices.Equal(bodies, []string{"c", "d"}) {
		t.Errorf("open findings = %v, want [c d]", bodies)
	}
}

func TestEngineWaitBlocksUntilRunSaved(t *testing.T) {
	env := newEngineEnv(t)
	env.snap.Number = 99 // a failing review still saves, which is what Wait covers
	atSave := make(chan struct{})
	release := make(chan struct{})
	env.e.beforeSave = func() {
		close(atSave)
		<-release
	}

	if err := env.e.Wait(context.Background()); err != nil {
		t.Fatalf("Wait with nothing running: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.e.Run(context.Background(), Request{PR: tracker.Tracked{PR: env.snap}, Mode: ModeAuto}, nil)
	}()
	<-atSave

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := env.e.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait during a run = %v, want deadline exceeded", err)
	}
	close(release)
	<-done
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := env.e.Wait(ctx2); err != nil {
		t.Fatalf("Wait after the run saved: %v", err)
	}
	if saved := env.stored(t); len(saved) != 1 {
		t.Errorf("stored = %d reviews, want 1", len(saved))
	}
}

// A stored slot of a repo with no mapped path and no clone is not worth a
// network clone: the rows go, nothing is cloned.
func TestPruneIdleNeverClones(t *testing.T) {
	env := newEngineEnv(t)
	var cloned []string
	env.e.CloneURL = func(repo string) string { cloned = append(cloned, repo); return env.s.upstream }
	ctx := context.Background()
	slot := store.Slot{Repo: "o/nope", Path: filepath.Join(env.data, "worktrees", "o__nope", "slot-1"), LastUsed: env.now.Add(-5 * time.Hour)}
	if err := env.st.SaveSlot(ctx, slot); err != nil {
		t.Fatal(err)
	}
	if err := env.e.PruneIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cloned) != 0 {
		t.Errorf("PruneIdle asked for a clone URL of %v", cloned)
	}
	if exists(filepath.Join(env.data, "clones", "o__nope.git")) {
		t.Error("PruneIdle cloned the repo")
	}
	if slots, _ := env.st.Slots(ctx, "o/nope"); len(slots) != 0 {
		t.Errorf("slot rows kept: %+v", slots)
	}
}

func TestHasIdleSlotFiltersPoolDir(t *testing.T) {
	env := newEngineEnv(t)
	ctx := context.Background()
	old := env.now.Add(-5 * time.Hour)
	if err := env.st.SaveSlot(ctx, store.Slot{Repo: "o/n", Path: filepath.Join(env.data, "elsewhere", "slot-1"), LastUsed: old}); err != nil {
		t.Fatal(err)
	}
	if due, err := env.e.hasIdleSlot(ctx, "o/n", time.Hour); err != nil || due {
		t.Errorf("hasIdleSlot = %v, %v for a slot outside the pool directory, want false", due, err)
	}
	if err := env.st.SaveSlot(ctx, store.Slot{Repo: "o/n", Path: filepath.Join(env.data, "worktrees", "o__n", "slot-1"), LastUsed: old}); err != nil {
		t.Fatal(err)
	}
	if due, err := env.e.hasIdleSlot(ctx, "o/n", time.Hour); err != nil || !due {
		t.Errorf("hasIdleSlot = %v, %v for an idle slot in the pool directory, want true", due, err)
	}
}
