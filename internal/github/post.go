package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
)

const threadIDsMaxPages = 5

// The one GitHub write the tool makes. It lives here, apart from Client, so
// nothing that polls or reviews can reach a mutation.
const addReviewMutation = `mutation($input: AddPullRequestReviewInput!) {
  addPullRequestReview(input: $input) {
    pullRequestReview {
      id
      url
      comments(first: 100) { nodes { id path line originalLine } }
    }
  }
}`

// Review threads are matched to comments afterwards because the mutation's
// payload does not return them. Pages run backwards: a review just posted
// sits at the end of the PR's threads.
const threadIDsQuery = `query($pr: ID!, $before: String) {
  node(id: $pr) {
    ... on PullRequest {
      reviewThreads(last: 100, before: $before) {
        pageInfo { hasPreviousPage startCursor }
        nodes { id comments(first: 1) { nodes { id } } }
      }
    }
  }
}`

type PostThread struct {
	Path      string
	Line      int
	StartLine int // 0 = single line
	Body      string
}

type PostInput struct {
	PRID, CommitOID, Event, Body string
	Threads                      []PostThread
}

// PostedComment is one comment of a posted review. Line is where it sits
// at the PR's head, 0 when that line is gone; OriginalLine is the line it
// was posted on, which is what a review of an older commit can match.
type PostedComment struct {
	ID, Path           string
	Line, OriginalLine int
}

type PostResult struct {
	ReviewID, URL string
	Comments      []PostedComment
}

type Poster interface {
	PostReview(ctx context.Context, in PostInput) (PostResult, error)
	// ThreadIDs maps comment IDs to the IDs of their review threads. A
	// comment it does not find is left out.
	ThreadIDs(ctx context.Context, prID string, commentIDs []string) (map[string]string, error)
}

// RefusedError is a post GitHub answered and declined, so nothing was
// posted and it is safe to send again.
type RefusedError struct{ Err error }

func (e *RefusedError) Error() string { return e.Err.Error() }
func (e *RefusedError) Unwrap() error { return e.Err }

// IsRefused reports whether a PostReview error means nothing was posted:
// GitHub answered with errors and no review, or with an HTTP 4xx, or the
// input was refused before anything was sent. Any other error, a timeout
// or a 5xx among them, may have come after GitHub took the review.
func IsRefused(err error) bool {
	if _, ok := errors.AsType[*RefusedError](err); ok {
		return true
	}
	var h *api.HTTPError
	return errors.As(err, &h) && h.StatusCode >= 400 && h.StatusCode < 500
}

type poster struct{ d Doer }

func NewPoster(d Doer) Poster { return &poster{d: d} }

// NewGHPoster builds a poster that authenticates with the user's gh login.
func NewGHPoster() (Poster, error) {
	g, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("github: gh auth: %w", err)
	}
	return NewPoster(g), nil
}

// PostReview sends every value as a variable, never inside the query text.
// A failure is returned as is, with GitHub's message, and never retried: a
// retry after a response was lost could post the review twice.
func (p *poster) PostReview(ctx context.Context, in PostInput) (PostResult, error) {
	// DISMISS and the empty event (a pending review) are never wanted, and a
	// review is always posted against the commit it reviewed.
	switch in.Event {
	case "APPROVE", "COMMENT", "REQUEST_CHANGES":
	default:
		return PostResult{}, &RefusedError{fmt.Errorf("github: post review: event %q is not APPROVE, COMMENT or REQUEST_CHANGES", in.Event)}
	}
	if in.CommitOID == "" {
		return PostResult{}, &RefusedError{errors.New("github: post review: no commit to post against")}
	}
	threads := make([]map[string]any, 0, len(in.Threads))
	for _, t := range in.Threads {
		m := map[string]any{"path": t.Path, "line": t.Line, "side": "RIGHT", "body": t.Body}
		if t.StartLine > 0 {
			m["startLine"] = t.StartLine
			m["startSide"] = "RIGHT"
		}
		threads = append(threads, m)
	}
	input := map[string]any{
		"pullRequestId": in.PRID,
		"commitOID":     in.CommitOID,
		"body":          in.Body,
		"event":         in.Event,
		"threads":       threads,
	}
	var resp struct {
		AddPullRequestReview *struct {
			PullRequestReview struct {
				ID       string `json:"id"`
				URL      string `json:"url"`
				Comments struct {
					Nodes []struct {
						ID           string `json:"id"`
						Path         string `json:"path"`
						Line         *int   `json:"line"`
						OriginalLine *int   `json:"originalLine"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"pullRequestReview"`
		} `json:"addPullRequestReview"`
	}
	if err := p.d.DoWithContext(ctx, addReviewMutation, map[string]any{"input": input}, &resp); err != nil {
		err = fmt.Errorf("github: post review: %w", err)
		// go-gh decodes the data before returning GraphQL errors, so a
		// null addPullRequestReview here is GitHub saying no.
		var gqlErr *api.GraphQLError
		if errors.As(err, &gqlErr) && resp.AddPullRequestReview == nil {
			return PostResult{}, &RefusedError{err}
		}
		return PostResult{}, err
	}
	if resp.AddPullRequestReview == nil {
		return PostResult{}, fmt.Errorf("github: post review: no review in the response")
	}
	r := resp.AddPullRequestReview.PullRequestReview
	res := PostResult{ReviewID: r.ID, URL: r.URL}
	for _, n := range r.Comments.Nodes {
		c := PostedComment{ID: n.ID, Path: n.Path}
		if n.Line != nil {
			c.Line = *n.Line
		}
		if n.OriginalLine != nil {
			c.OriginalLine = *n.OriginalLine
		}
		res.Comments = append(res.Comments, c)
	}
	return res, nil
}

func (p *poster) ThreadIDs(ctx context.Context, prID string, commentIDs []string) (map[string]string, error) {
	want := make(map[string]bool, len(commentIDs))
	for _, id := range commentIDs {
		want[id] = true
	}
	found := map[string]string{}
	var before any
	for range threadIDsMaxPages {
		if len(found) == len(want) {
			break
		}
		var resp struct {
			Node struct {
				ReviewThreads struct {
					PageInfo struct {
						HasPreviousPage bool   `json:"hasPreviousPage"`
						StartCursor     string `json:"startCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						ID       string `json:"id"`
						Comments struct {
							Nodes []struct {
								ID string `json:"id"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"node"`
		}
		if err := p.d.DoWithContext(ctx, threadIDsQuery, map[string]any{"pr": prID, "before": before}, &resp); err != nil {
			return nil, fmt.Errorf("github: review threads: %w", err)
		}
		rt := resp.Node.ReviewThreads
		for _, t := range rt.Nodes {
			if len(t.Comments.Nodes) > 0 && want[t.Comments.Nodes[0].ID] {
				found[t.Comments.Nodes[0].ID] = t.ID
			}
		}
		if !rt.PageInfo.HasPreviousPage {
			break
		}
		before = rt.PageInfo.StartCursor
	}
	return found, nil
}
