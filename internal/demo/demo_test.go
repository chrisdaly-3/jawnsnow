package demo

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
	"github.com/chrisdaly-3/jawnsnow/internal/tui"
)

var (
	_ github.Client = (*Client)(nil)
	_ github.Poster = (*Poster)(nil)
	_ tui.Reviewer  = (*Reviewer)(nil)
)

var testNow = time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)

func seeded(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "jawnsnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := Seed(t.Context(), st, testNow); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	return st
}

func TestSeed(t *testing.T) {
	ctx := t.Context()
	st := seeded(t)

	prs, err := st.LoadTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wall, done int
	for _, p := range prs {
		switch p.Place {
		case tracker.PlaceWall:
			wall++
		case tracker.PlaceDone:
			done++
		}
	}
	if wall < 8 || wall > 10 {
		t.Errorf("wall has %d PRs, want 8-10", wall)
	}
	if done < 1 {
		t.Errorf("done has %d PRs, want at least 1", done)
	}

	// The reviews the drawer shows: complete, at the PR's head, with
	// blocking and non-blocking findings, a suggestion and an unanchored note.
	var reviewed int
	var blocking, nonBlocking, suggestion, unanchored, markup bool
	for id, p := range prs {
		r, ok, err := st.LatestCompleted(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			continue
		}
		reviewed++
		if r.HeadOID != p.PR.HeadOID {
			t.Errorf("%s#%d: review is at %s, PR head is %s", p.PR.Repo, p.PR.Number, r.HeadOID, p.PR.HeadOID)
		}
		for _, f := range r.Findings {
			blocking = blocking || f.Blocking
			nonBlocking = nonBlocking || !f.Blocking
			suggestion = suggestion || strings.Contains(f.Body, "```suggestion")
			unanchored = unanchored || f.Unanchored
			markup = markup || (strings.Contains(f.Body, "**") && strings.Contains(f.Body, "`"))
		}
	}
	if reviewed < 2 || reviewed > 3 {
		t.Errorf("%d tracked PRs have a completed review, want 2-3", reviewed)
	}
	for name, ok := range map[string]bool{
		"blocking": blocking, "non-blocking": nonBlocking, "suggestion": suggestion,
		"unanchored": unanchored, "bold and inline code": markup,
	} {
		if !ok {
			t.Errorf("no seeded finding is %s", name)
		}
	}

	// The Cost view's ledger: 30 days of rows over several series, each
	// review a plausible price, each row belonging to a saved review.
	rows, err := st.CostRows(ctx, store.CostFilter{From: testNow.AddDate(0, 0, -30), To: testNow.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	models, tiers, authors, days := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	perReview := map[string]float64{}
	for _, r := range rows {
		models[r.Model], tiers[r.Tier], authors[r.Author] = true, true, true
		days[r.At.Format(time.DateOnly)] = true
		perReview[r.ReviewID] += r.CostUSD
		if rs, err := st.Reviews(ctx, r.PRID); err != nil || !slices.ContainsFunc(rs, func(rv store.Review) bool { return rv.ID == r.ReviewID }) {
			t.Errorf("cost row for review %s has no saved review (err %v)", r.ReviewID, err)
		}
	}
	if len(models) < 3 || len(tiers) < 2 || len(authors) < 2 {
		t.Errorf("ledger spans %d models, %d tiers, %d authors; want 3, 2, 2", len(models), len(tiers), len(authors))
	}
	if len(days) < 12 {
		t.Errorf("ledger rows fall on %d days, want them spread over the month", len(days))
	}
	for id, c := range perReview {
		if c < 0.05 || c > 2 {
			t.Errorf("review %s cost $%.2f, want $0.05-$2", id, c)
		}
	}
}

func TestClientReturnsEverySeededPR(t *testing.T) {
	ctx := t.Context()
	st := seeded(t)
	prs, err := st.LoadTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(testNow)

	viewer, err := c.Viewer(ctx)
	if err != nil || viewer != Viewer {
		t.Fatalf("Viewer() = %q, %v; want %q", viewer, err, Viewer)
	}
	ids := slices.Sorted(maps.Keys(prs))
	snaps, skipped, rl, err := c.Details(ctx, viewer, ids)
	if err != nil || len(skipped) > 0 {
		t.Fatalf("Details: skipped %v, err %v", skipped, err)
	}
	if len(snaps) != len(prs) {
		t.Errorf("Details returned %d snapshots for %d seeded PRs", len(snaps), len(prs))
	}
	for _, s := range snaps {
		if !prs[s.ID].PR.Fingerprint().Same(s.Fingerprint()) {
			t.Errorf("%s#%d: client snapshot differs from the seeded one", s.Repo, s.Number)
		}
	}
	healthy(t, "Details", rl)

	fps, skipped, rl, err := c.Fingerprints(ctx, ids)
	if err != nil || len(skipped) > 0 || len(fps) != len(ids) {
		t.Errorf("Fingerprints: %d of %d, skipped %v, err %v", len(fps), len(ids), skipped, err)
	}
	healthy(t, "Fingerprints", rl)

	// The default feed finds every open PR on the wall.
	feed := config.Defaults().Feeds[0]
	found, rl, err := c.Search(ctx, github.FeedQuery(feed, testNow))
	if err != nil {
		t.Fatal(err)
	}
	healthy(t, "Search", rl)
	for id, p := range prs {
		in := slices.ContainsFunc(found, func(f github.Fingerprint) bool { return f.ID == id })
		if open := p.PR.State == github.PROpen; in != open {
			t.Errorf("%s#%d: in search = %v, open = %v", p.PR.Repo, p.PR.Number, in, open)
		}
	}

	pc, rl, err := c.ReviewContext(ctx, ids[0])
	if err != nil || pc.Body == "" || len(pc.Files) == 0 {
		t.Errorf("ReviewContext = %+v, %v; want a body and files", pc, err)
	}
	healthy(t, "ReviewContext", rl)
}

func healthy(t *testing.T, call string, rl github.RateLimit) {
	t.Helper()
	if rl.Remaining < 1000 || !rl.ResetAt.After(testNow) {
		t.Errorf("%s rate limit = %+v, want a healthy one", call, rl)
	}
}

func TestSeededStatuses(t *testing.T) {
	st := seeded(t)
	prs, err := st.LoadTracked(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next, removed := tracker.Apply(prs, tracker.PollInput{Snapshots: NewClient(testNow).snapshots(), Viewer: Viewer, Now: testNow}, config.Defaults())
	if len(removed) > 0 {
		t.Errorf("a poll removed %v", removed)
	}
	seen := map[status.Status]bool{}
	spinning := false
	for _, p := range next {
		seen[p.Status.Status] = true
		spinning = spinning || (p.Place == tracker.PlaceWall && p.Status.Spinning)
	}
	if len(seen) < 6 {
		t.Errorf("seeded PRs have %d statuses (%v), want at least 6", len(seen), seen)
	}
	for _, want := range []status.Status{
		status.CIWaiting, status.CIFailed, status.ReadyForReview, status.NewChanges,
		status.ChangesRequested, status.Approved, status.ApprovedByYou, status.Merged,
	} {
		if !seen[want] {
			t.Errorf("no seeded PR is %s", want)
		}
	}
	if !spinning {
		t.Error("no wall PR spins")
	}
}

func TestPosterNeverFails(t *testing.T) {
	ctx := t.Context()
	p := NewPoster()
	for _, event := range []string{"APPROVE", "COMMENT", "REQUEST_CHANGES"} {
		in := github.PostInput{
			PRID: "PR_demo", CommitOID: "abc123", Event: event, Body: "Looks good.",
			Threads: []github.PostThread{{Path: "a.go", Line: 3, Body: "one"}, {Path: "b.go", Line: 9, StartLine: 7, Body: "two"}},
		}
		res, err := p.PostReview(ctx, in)
		if err != nil {
			t.Fatalf("PostReview(%s): %v", event, err)
		}
		if res.ReviewID == "" || len(res.Comments) != len(in.Threads) {
			t.Fatalf("PostReview(%s) = %+v, want an ID and a comment per thread", event, res)
		}
		var ids []string
		for i, c := range res.Comments {
			if c.Path != in.Threads[i].Path || c.OriginalLine != in.Threads[i].Line {
				t.Errorf("comment %d = %+v, want it on %s:%d", i, c, in.Threads[i].Path, in.Threads[i].Line)
			}
			ids = append(ids, c.ID)
		}
		threads, err := p.ThreadIDs(ctx, in.PRID, ids)
		if err != nil || len(threads) != len(ids) {
			t.Errorf("ThreadIDs = %v, %v; want one per comment", threads, err)
		}
	}
}

func TestReviewerSavesAReview(t *testing.T) {
	ctx := t.Context()
	st := seeded(t)
	prs, err := st.LoadTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var target tracker.Tracked
	for id, p := range prs {
		if _, ok, _ := st.LatestCompleted(ctx, id); !ok && p.Status.Status == status.ReadyForReview {
			target = p
		}
	}
	if target.PR.ID == "" {
		t.Fatal("no unreviewed PR in Ready for you")
	}
	r := &Reviewer{Store: st, Delay: time.Millisecond, Now: func() time.Time { return testNow }}
	var events int
	got, err := r.Run(ctx, review.Request{PR: target, Mode: review.ModeManual}, func(review.Event) { events++ })
	if err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Error("Run sent no progress events")
	}
	saved, ok, err := st.LatestCompleted(ctx, target.PR.ID)
	if err != nil || !ok || saved.ID != got.ID || len(saved.Findings) == 0 || saved.HeadOID != target.PR.HeadOID {
		t.Errorf("saved review = %+v, %v, %v; want the returned one, with findings, at the head", saved, ok, err)
	}
	rows, err := st.CostRows(ctx, store.CostFilter{})
	if err != nil || !slices.ContainsFunc(rows, func(c store.CostRow) bool { return c.ReviewID == got.ID }) {
		t.Errorf("no cost row for the review (err %v)", err)
	}
	if err := r.ReleasePR(ctx, target.PR.Repo, target.PR.Number); err != nil {
		t.Error(err)
	}
	if err := r.PruneIdle(ctx); err != nil {
		t.Error(err)
	}
}

func TestReviewerStopsOnCancel(t *testing.T) {
	st := seeded(t)
	prs, err := st.LoadTracked(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var target tracker.Tracked
	for _, p := range prs {
		target = p
		break
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := &Reviewer{Store: st, Delay: time.Hour}
	if _, err := r.Run(ctx, review.Request{PR: target, Mode: review.ModeManual}, func(review.Event) {}); err == nil {
		t.Error("Run on a cancelled context succeeded")
	}
}

// TestNoNetwork guards the package's promise: nothing in it can reach
// GitHub, Claude or any other host.
func TestNoNetwork(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{
		"net": true, "net/http": true, "os/exec": true, "syscall": true,
		"github.com/cli/go-gh/v2/pkg/api": true, "github.com/cli/go-gh/v2/pkg/auth": true,
		"github.com/chrisdaly-3/jawnsnow/internal/workspace": true,
	}
	constructors := map[string]bool{
		"NewGHClient": true, "NewGHPoster": true, "NewClient": true, "NewPoster": true,
		"DetectClaude": true, "Run": true, "Engine": true,
	}
	var checked int
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		aliases := map[string]string{} // local name → import path
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if banned[path] || strings.HasPrefix(path, "net/") {
				t.Errorf("%s imports %s", name, path)
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			aliases[local] = path
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path := aliases[pkg.Name]
			isGitHub := path == "github.com/chrisdaly-3/jawnsnow/internal/github"
			isReview := path == "github.com/chrisdaly-3/jawnsnow/internal/review"
			if (isGitHub || isReview) && constructors[sel.Sel.Name] {
				t.Errorf("%s: %s calls %s.%s", fset.Position(sel.Pos()), name, pkg.Name, sel.Sel.Name)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}
