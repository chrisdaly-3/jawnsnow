package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is the fake claude: the test binary re-executed. It
// ignores the claude flags, requires a non-empty prompt on stdin, and replays
// the fixture named by GO_FIXTURE. With GO_CALL_DIR set it records each call
// there and replays that call's numbered fixture instead (see recordCall).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	in, _ := io.ReadAll(os.Stdin)
	if len(strings.TrimSpace(string(in))) == 0 {
		os.Stderr.WriteString("empty stdin\n")
		os.Exit(2)
	}
	if pidFile := os.Getenv("GO_CHILD_PIDFILE"); pidFile != "" {
		child := exec.Command("sleep", "30")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	fixture := os.Getenv("GO_FIXTURE")
	if dir := os.Getenv("GO_CALL_DIR"); dir != "" {
		fixture = recordCall(dir, string(in))
	}
	if wait := os.Getenv("GO_WAIT_FILE"); wait != "" {
		// Hold the run open until the test creates the file.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(wait); err == nil {
				break
			}
		}
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(4)
	}
	os.Stdout.Write(fillPlaceholders(data))
	if os.Getenv("GO_EXIT_CODE") == "1" {
		os.Exit(1)
	}
	os.Exit(0)
}

func fakeSession(t *testing.T, fixture string) Session {
	t.Helper()
	env := []string{"GO_WANT_HELPER_PROCESS=1"}
	if fixture != "" {
		abs, err := filepath.Abs(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, "GO_FIXTURE="+abs)
	}
	return Session{
		binEnv:    env,
		Binary:    os.Args[0],
		binArgs:   []string{"-test.run=TestHelperProcess", "--"},
		Dir:       t.TempDir(),
		SessionID: NewID(),
		Timeout:   10 * time.Second,
	}
}

func TestBuildArgsExact(t *testing.T) {
	t.Parallel()
	s := Session{
		AddDirs: []string{"/d1", "/d2"}, Schema: `{"type":"object"}`,
		Model: "claude-opus-5-5", Effort: "high", MaxTurns: 100, MaxBudgetUSD: 15,
		SessionID: "abc",
	}
	want := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--tools", "Read,Grep,Glob", "--permission-mode", "dontAsk",
		"--setting-sources", "", "--strict-mcp-config",
		"--max-turns", "100", "--max-budget-usd", "15",
		"--json-schema", `{"type":"object"}`,
		"--model", "claude-opus-5-5", "--effort", "high",
		"--session-id", "abc",
		"--add-dir", "/d1", "--add-dir", "/d2",
	}
	if got := BuildArgs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("args:\n got %q\nwant %q", got, want)
	}
	s.Resume = true
	got := BuildArgs(s)
	for i, a := range got {
		if a == "--session-id" {
			t.Errorf("Resume still has --session-id")
		}
		if a == "--resume" && got[i+1] != "abc" {
			t.Errorf("--resume value = %q", got[i+1])
		}
	}
	if !contains(got, "--resume") {
		t.Errorf("Resume missing --resume: %q", got)
	}
}

// The reviewer runs with its working directory in the PR's checkout, so any
// settings source claude reads from the project would let the PR add hooks,
// env, MCP servers or instructions.
func TestBuildArgsNeverLoadsProjectSettings(t *testing.T) {
	t.Parallel()
	for _, resume := range []bool{false, true} {
		got := BuildArgs(Session{SessionID: "abc", Resume: resume, AddDirs: []string{"/d"}})
		for _, a := range got {
			if a == "local" || a == "project" {
				t.Errorf("resume=%v: args load a project settings source %q: %q", resume, a, got)
			}
		}
		i := slices.Index(got, "--setting-sources")
		if i < 0 || i+1 >= len(got) || got[i+1] != "" {
			t.Errorf("resume=%v: --setting-sources not followed by an empty argument: %q", resume, got)
		}
		if !slices.Contains(got, "--strict-mcp-config") {
			t.Errorf("resume=%v: --strict-mcp-config missing: %q", resume, got)
		}
	}
}

func TestRunParsesStream(t *testing.T) {
	t.Parallel()
	res, err := Run(context.Background(), fakeSession(t, "stream-ok.jsonl"), "review this", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.CostUSD != 0.42 || res.Turns != 3 || res.SessionID != "sess-1" || res.Subtype != "success" || res.IsError {
		t.Errorf("result = %+v", res)
	}
	if res.TokensIn != 120 || res.TokensOut != 50 || res.TokensCached != 700 {
		t.Errorf("tokens in/out/cached = %d/%d/%d", res.TokensIn, res.TokensOut, res.TokensCached)
	}
	if res.Duration != 1234*time.Millisecond {
		t.Errorf("duration = %v", res.Duration)
	}
	var out map[string]string
	if err := json.Unmarshal(res.Output, &out); err != nil || out["verdict"] != "approve" {
		t.Errorf("output = %s (%v)", res.Output, err)
	}
	if !reflect.DeepEqual(res.Reads, []string{"a.go", "b.go"}) {
		t.Errorf("reads = %v", res.Reads)
	}
	if !reflect.DeepEqual(res.Searched, []string{"internal/"}) {
		t.Errorf("searched = %v", res.Searched)
	}
}

func TestRunEmitsEvents(t *testing.T) {
	t.Parallel()
	var got []Event
	if _, err := Run(context.Background(), fakeSession(t, "stream-ok.jsonl"), "p", func(e Event) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	want := []Event{{"Read", "a.go"}, {"Grep", "internal/"}, {"Read", "b.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestRunHandlesHugeLine(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 3<<20)
	line := `{"type":"result","subtype":"success","is_error":false,"session_id":"s","structured_output":{"b":"` + big + `"},"total_cost_usd":1,"num_turns":1,"duration_ms":1,"usage":{}}` + "\n"
	p := filepath.Join(t.TempDir(), "big.jsonl")
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := fakeSession(t, "")
	s.binEnv = append(s.binEnv, "GO_FIXTURE="+p)
	res, err := Run(context.Background(), s, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) < 3<<20 {
		t.Errorf("output truncated: %d", len(res.Output))
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	s := fakeSession(t, "")
	s.binEnv = append(s.binEnv, "GO_CHILD_PIDFILE="+pidFile)
	s.Timeout = 300 * time.Millisecond
	start := time.Now()
	res, err := Run(context.Background(), s, "p", nil)
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	if !res.CostUnknown || res.CostUSD != 0 {
		t.Errorf("killed run: CostUnknown %v cost %v, want unknown and no number", res.CostUnknown, res.CostUSD)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("child pid not written: %v", rerr)
	}
	pid, _ := strconv.Atoi(string(b))
	deadline := time.Now().Add(2 * time.Second)
	for {
		if kerr := syscall.Kill(pid, 0); errors.Is(kerr, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d still alive after group kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunCancelKeepsResultSeen(t *testing.T) {
	t.Parallel()
	// The fixture has a result; cancelling afterwards must not discard it.
	ctx, cancel := context.WithCancel(context.Background())
	res, err := Run(ctx, fakeSession(t, "stream-ok.jsonl"), "p", func(Event) { cancel() })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.CostUSD != 0.42 || res.CostUnknown {
		t.Errorf("cost = %v, unknown %v", res.CostUSD, res.CostUnknown)
	}
}

func TestRunMissingResultIsError(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), fakeSession(t, "stream-noresult.jsonl"), "p", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	s := fakeSession(t, "stream-noresult.jsonl")
	s.binEnv = append(s.binEnv, "GO_EXIT_CODE=1")
	if _, err := Run(context.Background(), s, "p", nil); err == nil {
		t.Fatal("expected error on non-zero exit")
	}
}

func TestRunIsErrorResult(t *testing.T) {
	t.Parallel()
	res, err := Run(context.Background(), fakeSession(t, "stream-iserror.jsonl"), "p", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !res.IsError || res.Subtype != "error_max_turns" || res.CostUSD != 1.5 {
		t.Errorf("result = %+v", res)
	}
}

func TestNewIDIsV4(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := NewID(), NewID()
	if !re.MatchString(a) || a == b {
		t.Errorf("ids %q %q", a, b)
	}
}
