package review

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// MaxTurns is the most turns one claude run may take (--max-turns). A run is
// also bounded by review.budget.per_review_usd and review.run_timeout.
const MaxTurns = 100

// WorktreeIdle is how long a worktree may go unused before PruneIdle removes
// it.
const WorktreeIdle = 2 * time.Hour

// Session describes one claude invocation.
type Session struct {
	Binary, Dir  string
	AddDirs      []string
	Schema       string
	Model        string
	Effort       string
	MaxTurns     int
	MaxBudgetUSD float64
	SessionID    string
	Resume       bool
	Timeout      time.Duration

	// binArgs come before the claude flags; tests use them to re-execute the
	// test binary as a fake claude.
	binArgs []string
}

// BuildArgs returns the claude flags. The reviewer is always read-only:
// Read, Grep and Glob, no shell, no network, and no settings files.
//
// claude runs with its working directory in the PR's checkout, so every
// settings source it reads from the project is under the PR author's
// control: "local" alone loads .claude/settings.local.json (hooks, env, MCP
// servers) and CLAUDE.local.md from the PR. An empty --setting-sources
// value, passed as its own argument, loads none of them, and
// --strict-mcp-config ignores any .mcp.json. --bare would also do it but
// breaks auth.
func BuildArgs(s Session) []string {
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--tools", "Read,Grep,Glob",
		"--permission-mode", "dontAsk",
		"--setting-sources", "", "--strict-mcp-config",
		"--max-turns", strconv.Itoa(s.MaxTurns),
		"--max-budget-usd", strconv.FormatFloat(s.MaxBudgetUSD, 'f', -1, 64),
		"--json-schema", s.Schema,
		"--model", s.Model,
		"--effort", s.Effort,
	}
	if s.Resume {
		args = append(args, "--resume", s.SessionID)
	} else {
		args = append(args, "--session-id", s.SessionID)
	}
	for _, d := range s.AddDirs {
		args = append(args, "--add-dir", d)
	}
	return args
}

// Event is one tool call, for the progress ticker.
type Event struct{ Tool, Path string }

// RunResult is what a finished run reports.
type RunResult struct {
	SessionID                                string
	Output                                   json.RawMessage
	IsError                                  bool
	Subtype                                  string
	CostUSD                                  float64
	TokensIn, TokensOut, TokensCached, Turns int
	Duration                                 time.Duration
	Reads                                    []string // file_path of every Read call, in order
	Searched                                 []string // path of every Grep/Glob call
	// CostUnknown is set when claude ran but ended, killed or not, with no
	// result line: it spent something, and nothing says how much.
	CostUnknown bool
}

type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				FilePath string `json:"file_path"`
				Path     string `json:"path"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError          bool            `json:"is_error"`
	SessionID        string          `json:"session_id"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	DurationMS       int64           `json:"duration_ms"`
	Usage            struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Run starts claude with the prompt on stdin and parses its stream-json. It
// runs claude in its own process group and kills the whole group on context
// cancel or timeout. If a result line was already seen, the result is returned
// even when the run was cancelled, so its cost is recorded.
func Run(ctx context.Context, s Session, prompt string, onEvent func(Event)) (RunResult, error) {
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}

	args := append(append([]string{}, s.binArgs...), BuildArgs(s)...)
	cmd := exec.Command(s.Binary, args...)
	cmd.Dir = s.Dir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start %s: %w", s.Binary, err)
	}

	// Kill the group (not just the leader) so claude's children die too.
	pid := cmd.Process.Pid
	done := make(chan struct{})
	killed := make(chan struct{})
	go func() {
		defer close(killed)
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		case <-done:
		}
	}()

	var res RunResult
	var sawResult bool
	parseErr := parseStream(stdout, onEvent, &res, &sawResult)
	// Drain so the process is never blocked on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	close(done)
	<-killed
	res.CostUnknown = !sawResult

	switch {
	case sawResult:
		if res.IsError {
			return res, fmt.Errorf("claude reported an error result (%s)", res.Subtype)
		}
		return res, nil
	case ctx.Err() != nil:
		return res, fmt.Errorf("claude run stopped: %w", ctx.Err())
	case parseErr != nil:
		return res, parseErr
	case waitErr != nil:
		return res, fmt.Errorf("claude failed with no result: %w: %s", waitErr, tail(stderr.String()))
	default:
		return res, errors.New("claude exited without a result line")
	}
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[len(s)-500:]
	}
	return s
}

// parseStream reads one JSON object per line. Lines that are not JSON, or
// whose type is unknown, are skipped. bufio.Reader has no line-length limit,
// which matters because structured_output can be large.
func parseStream(r io.Reader, onEvent func(Event), res *RunResult, sawResult *bool) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			handleLine(line, onEvent, res, sawResult)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func handleLine(line []byte, onEvent func(Event), res *RunResult, sawResult *bool) {
	var l streamLine
	if err := json.Unmarshal(line, &l); err != nil {
		return
	}
	switch l.Type {
	case "assistant":
		for _, c := range l.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			var path string
			switch c.Name {
			case "Read":
				path = c.Input.FilePath
				res.Reads = append(res.Reads, path)
			case "Grep", "Glob":
				path = c.Input.Path
				res.Searched = append(res.Searched, path)
			default:
				continue
			}
			if onEvent != nil {
				onEvent(Event{Tool: c.Name, Path: path})
			}
		}
	case "result":
		res.SessionID = l.SessionID
		res.Output = l.StructuredOutput
		res.IsError = l.IsError
		res.Subtype = l.Subtype
		res.CostUSD = l.TotalCostUSD
		res.Turns = l.NumTurns
		res.Duration = time.Duration(l.DurationMS) * time.Millisecond
		res.TokensIn = l.Usage.InputTokens + l.Usage.CacheCreationTokens
		res.TokensOut = l.Usage.OutputTokens
		res.TokensCached = l.Usage.CacheReadTokens
		*sawResult = true
	}
}
