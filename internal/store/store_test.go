package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "dir", "snow.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sample(id string, n int) tracker.Tracked {
	at := time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)
	return tracker.Tracked{
		PR: github.Snapshot{
			ID: id, Repo: "acme/widgets", Number: n, Title: "Fix " + id, Author: "sam",
			State: github.PROpen, HeadOID: "abc123", UpdatedAt: at,
			Labels:  []string{"bug"},
			Checks:  []github.Check{{Name: "ci", State: github.CheckSuccess, Required: true}},
			Commits: []github.Commit{{OID: "abc123", Parents: []string{"def456"}}},
		},
		Status: status.Result{
			Status: status.Status("MERGED"),
			CI:     status.CIPending,
			YourReview: &github.Review{
				Author: "me", State: "APPROVED", CommitOID: "abc123", SubmittedAt: at,
			},
		},
		Feeds:     []string{"default"},
		Place:     tracker.PlaceWall,
		FirstSeen: at, LastSeen: at.Add(time.Minute),
	}
}

// normalize drops the monotonic clock reading and Location that a JSON
// round trip does not preserve, so DeepEqual compares instants.
func normalize(m map[string]tracker.Tracked) map[string]tracker.Tracked {
	out := make(map[string]tracker.Tracked, len(m))
	for k, v := range m {
		v.PR.UpdatedAt = v.PR.UpdatedAt.UTC().Round(0)
		v.FirstSeen = v.FirstSeen.UTC().Round(0)
		v.LastSeen = v.LastSeen.UTC().Round(0)
		v.HeadSeenAt = v.HeadSeenAt.UTC().Round(0)
		v.DoneAt = v.DoneAt.UTC().Round(0)
		v.ReturnedAt = v.ReturnedAt.UTC().Round(0)
		v.WaitingSince = v.WaitingSince.UTC().Round(0)
		v.PR.HeadCommittedAt = v.PR.HeadCommittedAt.UTC().Round(0)
		v.PR.MergedAt = v.PR.MergedAt.UTC().Round(0)
		v.PR.ClosedAt = v.PR.ClosedAt.UTC().Round(0)
		if r := v.Status.YourReview; r != nil {
			c := *r
			c.SubmittedAt = c.SubmittedAt.UTC().Round(0)
			v.Status.YourReview = &c
		}
		out[k] = v
	}
	return out
}

func TestRoundTripTracked(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	want := map[string]tracker.Tracked{"A": sample("A", 1), "B": sample("B", 2)}
	if err := s.SaveTracked(ctx, []tracker.Tracked{want["A"], want["B"]}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveUpserts(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a, b := sample("A", 1), sample("B", 2)
	if err := s.SaveTracked(ctx, []tracker.Tracked{a, b}); err != nil {
		t.Fatal(err)
	}
	a.Place = tracker.PlaceDone
	if err := s.SaveTracked(ctx, []tracker.Tracked{a}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got["A"].Place != tracker.PlaceDone {
		t.Errorf("A.Place = %q, want done", got["A"].Place)
	}
}

func TestDelete(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.SaveTracked(ctx, []tracker.Tracked{sample("A", 1), sample("B", 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTracked(ctx, []string{"A", "missing"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["A"]; ok || len(got) != 1 {
		t.Errorf("after delete got %v, want only B", got)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	s, path := openTemp(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_version SET version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = Open(path)
	if err == nil || !strings.Contains(err.Error(), "newer than this snow") {
		t.Fatalf("Open error = %v, want newer-than-this-snow", err)
	}
	if want := fmt.Sprintf("database schema v99 is newer than this snow (v%d); upgrade snow", len(migrations)); err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestReopenKeepsData(t *testing.T) {
	s, path := openTemp(t)
	if err := s.SaveTracked(context.Background(), []tracker.Tracked{sample("A", 1)}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.LoadTracked(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v; want 1 row", got, err)
	}
}

func TestDataPathXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/x")
	if got, want := DataPath(), "/x/jawnsnow/jawnsnow.db"; got != want {
		t.Errorf("DataPath = %q, want %q", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := DataPath(), "/home/u/.local/share/jawnsnow/jawnsnow.db"; got != want {
		t.Errorf("DataPath = %q, want %q", got, want)
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	s, _ := openTemp(t)
	var mode string
	var timeout int
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
		t.Errorf("busy_timeout = %d, %v; want 5000", timeout, err)
	}
}

func TestReopenKeepsSchemaVersion(t *testing.T) {
	_, path := openTemp(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	var rows, version int
	if err := s.db.QueryRow(`SELECT COUNT(*), MAX(version) FROM schema_version`).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || version != len(migrations) {
		t.Errorf("schema_version has %d rows at v%d; want 1 at v%d", rows, version, len(migrations))
	}
}
