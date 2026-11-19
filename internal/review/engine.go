package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/review/prompt"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

// ErrHeadMoved is returned, with no review saved, when the PR's head was
// force-pushed after the poll the review was planned from: the fetched head
// is not the snapshot's, and the snapshot's is gone. The next poll plans a
// review of the new head.
var ErrHeadMoved = errors.New("the PR's head moved since the last poll")

// maxContinuations is how many times one pass resumes its session to fill
// reading gaps or reword findings.
const maxContinuations = 2

// Outcomes of a review and of each run in it.
const (
	outcomeComplete   = "complete"
	outcomeIncomplete = "incomplete"
	outcomeFailed     = "failed"
)

// Request is one review to run.
type Request struct {
	PR      tracker.Tracked
	Mode    Mode
	Force   bool // always a full pass, even when an incremental one would do
	Session SessionTiers
	OneRun  *config.Tier
}

// Engine turns one PR into a saved review: it fetches the PR, checks it out
// in a pooled worktree, runs claude with continuations, checks the output
// and saves the review, its findings and a cost row per run. Run blocks
// for the whole review, so callers run it off the UI goroutine.
type Engine struct {
	Config          func() config.Config
	Client          github.Client
	Store           *store.Store
	DataDir, Claude string
	Viewer          func() string
	Now             func() time.Time
	CloneURL        func(string) string // nil → https://github.com/<name>.git

	binArgs    []string // tests run the test binary as a fake claude
	beforeSave func()   // tests widen the gap between a review and its save

	mu    sync.Mutex
	data  string               // DataDir with symlinks resolved, once created
	repos map[string]*repoPool // by "owner/name"

	runMu    sync.Mutex
	inFlight int           // Runs between their start and their save
	idle     chan struct{} // closed when inFlight returns to 0; set while it is above 0

	repoLocks keyedLock // one Resolve per repo at a time
	prLocks   keyedLock // a PR's workspace work and its release never overlap
}

type repoPool struct {
	repo workspace.Repo
	pool *workspace.Pool
}

// Run reviews req.PR and saves the result. A review whose pipeline fails
// at any step is saved as failed with the error text, and the error is
// returned with it. A continuation that fails after an earlier run gave
// parseable output is the exception: that output is kept and the review is
// incomplete. Cost rows and the saved review are written even after
// ctx is cancelled, so a stopped run's spend is still counted. ErrHeadMoved
// saves nothing.
func (e *Engine) Run(ctx context.Context, req Request, onEvent func(Event)) (store.Review, error) {
	defer e.runStarted()()
	pr := req.PR.PR
	rv := store.Review{
		ID: NewID(), PRID: pr.ID, Repo: pr.Repo, Number: pr.Number, Author: pr.Author,
		HeadOID: pr.HeadOID, Mode: string(req.Mode), StartedAt: e.now(),
	}
	// The PR's lock covers everything from reading the last review to
	// saving this one: a second Run then plans against this review, and a
	// ReleasePR waiting on the lock sees this review's ID and removes its
	// files. Lock order is PR, then repo, as in ReleasePR.
	unlock, err := e.prLocks.lock(ctx, prKey(pr.Repo, pr.Number))
	if err == nil {
		defer unlock()
		err = e.review(ctx, req, &rv, onEvent)
	}
	if errors.Is(err, ErrHeadMoved) {
		return rv, err // nothing ran, and the next poll plans the new head
	}
	if err != nil {
		rv.Outcome, rv.Error, rv.Findings = outcomeFailed, err.Error(), nil
	}
	if e.beforeSave != nil {
		e.beforeSave()
	}
	rv.EndedAt = e.now()
	if serr := e.Store.SaveReview(context.WithoutCancel(ctx), rv); serr != nil {
		return rv, errors.Join(err, fmt.Errorf("save review: %w", serr))
	}
	return rv, err
}

// runStarted counts a Run as in flight and returns the func that ends it.
// Run defers it first, so it fires after the review is saved.
func (e *Engine) runStarted() func() {
	e.runMu.Lock()
	if e.inFlight == 0 {
		e.idle = make(chan struct{})
	}
	e.inFlight++
	e.runMu.Unlock()
	return func() {
		e.runMu.Lock()
		defer e.runMu.Unlock()
		if e.inFlight--; e.inFlight == 0 {
			close(e.idle)
		}
	}
}

// Wait blocks until no Run is in flight, which is after each has saved its
// review, or until ctx ends. Shutdown calls it before closing the store.
func (e *Engine) Wait(ctx context.Context) error {
	e.runMu.Lock()
	if e.inFlight == 0 {
		e.runMu.Unlock()
		return nil
	}
	idle := e.idle
	e.runMu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// review runs the pipeline and fills rv. It returns the error that fails
// the review. The caller holds the PR's lock.
func (e *Engine) review(ctx context.Context, req Request, rv *store.Review, onEvent func(Event)) error {
	cfg := e.Config()
	pr := req.PR.PR

	pc, _, err := e.Client.ReviewContext(ctx, pr.ID)
	if err != nil {
		return fmt.Errorf("fetch PR context: %w", err)
	}

	viewer := ""
	if e.Viewer != nil {
		viewer = e.Viewer()
	}
	choice := Resolve(cfg.Review, ResolveInput{
		Repo: pr.Repo, Author: pr.Author, Labels: pr.Labels, Files: pc.Files,
		Lines:         pr.Additions + pr.Deletions,
		HasYourReview: status.YourLatestReview(pr.Reviews, viewer) != nil,
		Session:       req.Session, OneRun: req.OneRun,
	})
	rv.Tier, rv.Model, rv.Effort = string(choice.Tier), choice.Model, choice.Effort

	prev, hasPrev, err := e.Store.LatestCompleted(ctx, pr.ID)
	if err != nil {
		return fmt.Errorf("load the last review: %w", err)
	}
	pass := PassFull
	if !req.Force && hasPrev && prev.MergeBaseOID != "" && incremental(pr, prev.HeadOID) {
		pass = PassIncremental
	}
	rv.PassKind = string(pass)
	var earlier []store.Finding
	if hasPrev {
		earlier = openFindings(prev.Findings)
	}

	rp, err := e.repoPool(ctx, pr.Repo)
	if err != nil {
		return err
	}

	head, base, err := rp.repo.FetchPR(ctx, pr.Number, pr.BaseRef)
	if err != nil {
		return fmt.Errorf("fetch PR: %w", err)
	}
	if pr.HeadOID != "" && head != pr.HeadOID {
		// A push since the poll: review the head the plan and its quiet
		// period were for, if the fetch brought it.
		if !rp.repo.HasCommit(ctx, pr.HeadOID) {
			return ErrHeadMoved
		}
		head = pr.HeadOID
	}
	rv.HeadOID, rv.BaseOID = head, base
	mergeBase, err := rp.repo.MergeBase(ctx, head, base)
	if err != nil {
		return fmt.Errorf("merge-base: %w", err)
	}
	rv.MergeBaseOID = mergeBase
	in := workspace.FilesInput{MergeBase: mergeBase, Head: head}
	if pass == PassIncremental {
		in.LastReviewed, in.LastMergeBase = prev.HeadOID, prev.MergeBaseOID
	}

	worktree, release, err := rp.pool.Acquire(ctx, pr.Number, head)
	if err != nil {
		return fmt.Errorf("acquire worktree: %w", err)
	}
	defer release()

	data, err := e.dataDir()
	if err != nil {
		return err
	}
	reviewDir := filepath.Join(data, "reviews", rv.ID)
	if err := os.MkdirAll(reviewDir, 0o755); err != nil {
		return fmt.Errorf("write review files: %w", err)
	}
	files, err := workspace.WriteReviewFiles(ctx, rp.repo, reviewDir, in)
	if err != nil {
		return fmt.Errorf("write review files: %w", err)
	}
	if files.Since == "" {
		// The last reviewed commits are gone, so there is nothing to diff
		// against; Earlier still goes in for the ledger.
		pass = PassFull
		rv.PassKind = string(pass)
	}
	standards, err := workspace.LoadStandards(ctx, rp.repo, base, files.Changed)
	if err != nil {
		return fmt.Errorf("load standards: %w", err)
	}
	guide, hasGuide, err := workspace.LoadSecurityGuide(ctx, rp.repo, base, reviewDir)
	if err != nil {
		return fmt.Errorf("load security guide: %w", err)
	}

	rc := cfg.RepoFor(pr.Repo)
	var contextDirs []string
	for _, c := range rc.Context {
		contextDirs = append(contextDirs, expandHome(c))
	}
	pin := PromptInput{
		Repo: pr.Repo, Number: pr.Number, Title: pr.Title, Author: pr.Author, URL: pr.URL,
		Context: pc, Files: files, Worktree: worktree, ContextRepos: contextDirs,
		Scope: rc.Scope, Standards: standards, Focus: rc.Focus, DesignRefs: rc.DesignRefs,
		Earlier: earlier, Pass: pass,
	}
	if hasGuide {
		pin.Security = &guide
	}
	// reviewer.md is the system prompt. It leads the message because the
	// claude flags in use pass no system prompt of their own.
	text := prompt.Reviewer() + "\n" + BuildPrompt(pin)

	sess := Session{
		Binary:       e.Claude,
		Dir:          worktree,
		AddDirs:      append([]string{reviewDir}, contextDirs...),
		Schema:       prompt.Schema(),
		Model:        choice.Model,
		Effort:       choice.Effort,
		MaxTurns:     cfg.Review.MaxTurns,
		MaxBudgetUSD: choice.PerReviewUSD,
		SessionID:    NewID(),
		Timeout:      cfg.Review.RunTimeout.Duration,
		binArgs:      e.binArgs,
	}
	rv.SessionID = sess.SessionID

	var (
		seen       RunResult // every Read and search of the session so far
		out        Output
		raw        json.RawMessage
		gaps, lint []string
		kept       []store.Finding
		notes      []string
		coverNote  string // which changed files the per-file rule checked
		runOutcome string
		stopped    string // why the loop stopped early, for the note
	)
	for continuations := 0; ; continuations++ {
		start := time.Now()
		res, runErr := Run(ctx, sess, text, onEvent)
		took := time.Since(start)
		// total_cost_usd is the session's running total, so a resumed
		// run's own cost is what the total grew by.
		runCost := max(res.CostUSD-rv.CostUSD, 0)
		rv.CostUSD = max(rv.CostUSD, res.CostUSD)
		rv.CostUnknown = rv.CostUnknown || res.CostUnknown
		seen.Reads = append(seen.Reads, res.Reads...)
		seen.Searched = append(seen.Searched, res.Searched...)
		var parsed Output
		if runErr == nil {
			parsed, runErr = ParseOutput(res.Output)
		}
		runOutcome = outcomeFailed
		if runErr == nil {
			out, raw = parsed, res.Output
			gaps, coverNote = ReviewGaps(pass, rc.Scope, files, worktree, seen, out)
			kept, notes, lint = CheckFindings(out, files)
			runOutcome = outcomeComplete
			if len(gaps) > 0 {
				runOutcome = outcomeIncomplete
			}
		}
		if err := e.addCost(ctx, rv, res, runCost, runOutcome, took); err != nil {
			return errors.Join(runErr, err)
		}
		if runErr != nil && raw == nil {
			return runErr
		}
		if runErr != nil {
			// An earlier run of this pass gave a usable review; failing the
			// whole review would pay for it again on retry.
			stopped = fmt.Sprintf("continuation %d failed: %v; showing the earlier pass", continuations, runErr)
			break
		}
		if len(gaps)+len(lint) == 0 || continuations == maxContinuations {
			break
		}
		left := choice.PerReviewUSD - rv.CostUSD
		if left <= 0 {
			stopped = "stopped: review budget used"
			break
		}
		asks := slices.Clone(gaps)
		for _, l := range lint {
			asks = append(asks, "reword: "+l)
		}
		text = ContinuationPrompt(asks)
		sess.Resume, sess.MaxBudgetUSD = true, left
	}

	rv.Outcome = runOutcome
	if runOutcome == outcomeFailed {
		// Only a continuation gets here failed: the review keeps the last
		// parseable run's output.
		rv.Outcome = outcomeIncomplete
	}
	if coverNote != "" {
		notes = append(notes, coverNote)
	}
	if stopped != "" {
		notes = append(notes, stopped)
	}
	rv.Verdict, rv.VerdictReason, rv.Understanding = out.Verdict, out.VerdictReason, out.Understanding
	rv.Assumptions = out.Assumptions
	if rv.Raw, err = withExamined(raw, VerifiedExamined(out.Examined, seen, worktree)); err != nil {
		return err
	}
	rv.Findings = append(kept, applyLedger(earlier, out.Ledger)...)
	rv.Note = joinNotes(out.Note, notes, gaps, lint)
	return nil
}

// incremental reports whether a pass after a review at prevHead need only
// read what changed since: the PR still has that commit, its head has
// moved, and not only by merging the base branch.
func incremental(pr github.Snapshot, prevHead string) bool {
	has := slices.ContainsFunc(pr.Commits, func(c github.Commit) bool { return c.OID == prevHead })
	return has && prevHead != pr.HeadOID && !status.OnlyBaseMergesSince(pr.Commits, prevHead)
}

// openFindings are the findings of a review the next pass must account for:
// all but those already addressed or answered.
func openFindings(fs []store.Finding) []store.Finding {
	var out []store.Finding
	for _, f := range fs {
		if f.Ledger != "addressed" && f.Ledger != "answered" {
			out = append(out, f)
		}
	}
	return out
}

// applyLedger returns copies of earlier with the reviewer's ledger states,
// ready to save with a new review. A finding the ledger leaves out, or
// marks answered without a posted thread the author could have answered
// in, is outstanding.
func applyLedger(earlier []store.Finding, ledger []LedgerEntry) []store.Finding {
	states := make(map[int64]string, len(ledger))
	for _, l := range ledger {
		states[l.ID] = l.State
	}
	out := make([]store.Finding, 0, len(earlier))
	for _, f := range earlier {
		state := states[f.ID]
		switch {
		case state == "addressed":
		case state == "answered" && f.ThreadID != "":
		default:
			state = "outstanding"
		}
		f.ID, f.ReviewID, f.Ledger = 0, "", state
		out = append(out, f)
	}
	return out
}

// withExamined returns the reviewer's output with examined replaced by ex,
// so what is stored and shown claims only reads that happened.
func withExamined(raw json.RawMessage, ex Examined) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse review output: %w", err)
	}
	orEmpty := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	b, err := json.Marshal(map[string][]string{
		"docs": orEmpty(ex.Docs), "code": orEmpty(ex.Code),
		"usages": orEmpty(ex.Usages), "tests": orEmpty(ex.Tests),
	})
	if err != nil {
		return nil, err
	}
	m["examined"] = b
	return json.Marshal(m)
}

// joinNotes is the review's note: the reviewer's own line, then the
// checks' notes, then the reading and rewording the last continuation
// left undone.
func joinNotes(reviewer string, checks, gaps, lint []string) string {
	var lines []string
	if s := strings.TrimSpace(reviewer); s != "" {
		lines = append(lines, s)
	}
	lines = append(lines, checks...)
	for _, g := range gaps {
		lines = append(lines, "not done: "+g)
	}
	for _, l := range lint {
		lines = append(lines, "wording: "+l)
	}
	return strings.Join(lines, "\n")
}

func (e *Engine) addCost(ctx context.Context, rv *store.Review, res RunResult, cost float64, outcome string, took time.Duration) error {
	d := res.Duration
	if d == 0 {
		d = took // a run that stopped before its result line
	}
	err := e.Store.AddCost(context.WithoutCancel(ctx), store.CostRow{
		At: e.now(), ReviewID: rv.ID, PRID: rv.PRID, Repo: rv.Repo, Author: rv.Author,
		Tier: rv.Tier, PassKind: rv.PassKind, Mode: rv.Mode, Model: rv.Model, Effort: rv.Effort,
		Outcome: outcome, CostUSD: cost, CostUnknown: res.CostUnknown,
		TokensIn: res.TokensIn, TokensOut: res.TokensOut, TokensCached: res.TokensCached,
		Duration: d,
	})
	if err != nil {
		return fmt.Errorf("record cost: %w", err)
	}
	return nil
}

// Pool returns the worktree pool for repo, resolving or cloning the repo
// the first time. There is one pool per repo for the life of the Engine.
func (e *Engine) Pool(ctx context.Context, repo string) (*workspace.Pool, error) {
	rp, err := e.repoPool(ctx, repo)
	if err != nil {
		return nil, err
	}
	return rp.pool, nil
}

func (e *Engine) cachedPool(name string) *repoPool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.repos[name]
}

func (e *Engine) repoPool(ctx context.Context, name string) (*repoPool, error) {
	if rp := e.cachedPool(name); rp != nil {
		return rp, nil
	}
	// Two bare clones into one destination would race, so one caller at a
	// time resolves a repo.
	unlock, err := e.repoLocks.lock(ctx, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if rp := e.cachedPool(name); rp != nil {
		return rp, nil
	}
	data, err := e.dataDir()
	if err != nil {
		return nil, err
	}
	cfg := e.Config()
	r, err := workspace.Resolve(ctx, name, cfg.RepoFor(name), data, e.cloneURL)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(data, "worktrees", strings.ReplaceAll(name, "/", "__"))
	rp := &repoPool{repo: r, pool: workspace.NewPool(r, dir, max(cfg.Review.MaxInFlight, 1), e.Store, e.now)}
	e.mu.Lock()
	if e.repos == nil {
		e.repos = map[string]*repoPool{}
	}
	e.repos[name] = rp
	e.mu.Unlock()
	return rp, nil
}

// ReleasePR deletes PR number's refs and review files and frees its slot.
// When this process has no pool for repo and the PR was never reviewed,
// there is nothing to release, so no repo is cloned for it.
func (e *Engine) ReleasePR(ctx context.Context, repo string, number int) error {
	unlock, err := e.prLocks.lock(ctx, prKey(repo, number))
	if err != nil {
		return err
	}
	defer unlock()
	ids, err := e.Store.ReviewIDsOf(ctx, repo, number)
	if err != nil {
		return err
	}
	rp := e.cachedPool(repo)
	if rp == nil {
		if len(ids) == 0 {
			return nil
		}
		if rp, err = e.repoPool(ctx, repo); err != nil {
			return err
		}
	}
	data, err := e.dataDir()
	if err != nil {
		return err
	}
	dirs := make([]string, len(ids))
	for i, id := range ids {
		dirs[i] = filepath.Join(data, "reviews", id)
	}
	return rp.pool.ReleasePR(ctx, number, dirs)
}

// PruneIdle removes worktrees idle for longer than review.worktree_idle
// from every repo with stored slots, as well as every pool this Engine has
// made. A repo this process never reviewed gets its pool only when it has
// a slot to prune; one that can't be resolved is skipped with its error.
func (e *Engine) PruneIdle(ctx context.Context) error {
	idle := e.Config().Review.WorktreeIdle.Duration
	names, err := e.Store.SlotRepos(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	for name := range e.repos {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	e.mu.Unlock()
	slices.Sort(names)

	var errs []error
	for _, name := range names {
		rp := e.cachedPool(name)
		if rp == nil {
			due, err := e.hasIdleSlot(ctx, name, idle)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !due {
				continue
			}
			if rp, err = e.repoPool(ctx, name); err != nil {
				errs = append(errs, fmt.Errorf("prune %s: %w", name, err))
				continue
			}
		}
		errs = append(errs, rp.pool.PruneIdle(ctx, idle))
	}
	return errors.Join(errs...)
}

// hasIdleSlot reports whether repo has a stored free slot unused for longer
// than idle.
func (e *Engine) hasIdleSlot(ctx context.Context, repo string, idle time.Duration) (bool, error) {
	slots, err := e.Store.Slots(ctx, repo)
	if err != nil {
		return false, err
	}
	now := e.now()
	return slices.ContainsFunc(slots, func(s store.Slot) bool {
		return !s.Busy && now.Sub(s.LastUsed) > idle
	}), nil
}

// dataDir creates DataDir and returns its real path. Pools and review
// directories are built from it, so cleanup's containment checks and the
// reviewer's absolute read paths agree with it.
func (e *Engine) dataDir() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.data != "" {
		return e.data, nil
	}
	if err := os.MkdirAll(e.DataDir, 0o755); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(e.DataDir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	e.data = real
	return real, nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) cloneURL(name string) string {
	if e.CloneURL != nil {
		return e.CloneURL(name)
	}
	return "https://github.com/" + name + ".git"
}

func prKey(repo string, number int) string { return repo + "#" + strconv.Itoa(number) }

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// keyedLock is a mutex per key whose lock gives up when its context ends.
type keyedLock struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (k *keyedLock) lock(ctx context.Context, key string) (unlock func(), err error) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]chan struct{}{}
	}
	ch, ok := k.m[key]
	if !ok {
		ch = make(chan struct{}, 1)
		k.m[key] = ch
	}
	k.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
