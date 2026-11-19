package demo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// Poster is a github.Poster that posts nowhere. It answers every valid
// post with a made-up review and one comment per thread, so the post flow
// can finish in a recording.
type Poster struct {
	mu sync.Mutex
	n  int
}

func NewPoster() *Poster { return &Poster{} }

// PostReview refuses what the real poster refuses before sending, and
// accepts the rest.
func (p *Poster) PostReview(_ context.Context, in github.PostInput) (github.PostResult, error) {
	switch in.Event {
	case "APPROVE", "COMMENT", "REQUEST_CHANGES":
	default:
		return github.PostResult{}, &github.RefusedError{Err: fmt.Errorf("demo: event %q is not APPROVE, COMMENT or REQUEST_CHANGES", in.Event)}
	}
	if in.CommitOID == "" {
		return github.PostResult{}, &github.RefusedError{Err: errors.New("demo: no commit to post against")}
	}
	p.mu.Lock()
	p.n++
	n := p.n
	p.mu.Unlock()
	res := github.PostResult{ReviewID: fmt.Sprintf("PRR_demo_%d", n)}
	for _, s := range snapshots(time.Time{}) {
		if s.ID == in.PRID {
			res.URL = fmt.Sprintf("%s#pullrequestreview-%d", s.URL, n)
		}
	}
	for i, t := range in.Threads {
		res.Comments = append(res.Comments, github.PostedComment{
			ID: fmt.Sprintf("PRRC_demo_%d_%d", n, i), Path: t.Path, Line: t.Line, OriginalLine: t.Line,
		})
	}
	return res, nil
}

// ThreadIDs gives each comment a thread of its own.
func (p *Poster) ThreadIDs(_ context.Context, _ string, commentIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(commentIDs))
	for _, id := range commentIDs {
		out[id] = "PRRT_" + id
	}
	return out, nil
}
