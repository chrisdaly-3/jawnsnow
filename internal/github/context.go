package github

import (
	"context"
	"encoding/json"
	"time"
)

const (
	contextMaxFilePages = 5 // 500 files
)

// TextComment is a PR conversation or review-thread comment. Author is empty
// for a deleted user.
type TextComment struct {
	Author, Body string
	At           time.Time
}

// Thread is a review thread. Line is 0 for an outdated thread, which GitHub
// reports with a null line.
type Thread struct {
	ID, Path string
	Line     int
	Resolved bool
	Comments []TextComment
}

// PRContext is what a review reads about a PR besides its diff.
type PRContext struct {
	Body     string
	Comments []TextComment
	Threads  []Thread
	Files    []string
}

const commentNodes = `nodes{author{login} body createdAt}`

const filesSelection = `files(first:100, after: $after){pageInfo{hasNextPage endCursor} nodes{path}}`

// reviewContextQuery fetches the body, comments, threads and the first page of
// files. It only reads: the client never writes.
const reviewContextQuery = `query ReviewContext($id: ID!, $after: String) {
  node(id: $id) { ... on PullRequest {
    body
    comments(last:50){` + commentNodes + `}
    reviewThreads(last:50){nodes{id isResolved path line comments(first:20){` + commentNodes + `}}}
    ` + filesSelection + `
  } }
  ` + rateLimitFields + `
}`

// reviewFilesPageQuery fetches a later page of changed files only.
const reviewFilesPageQuery = `query ReviewFilesPage($id: ID!, $after: String) {
  node(id: $id) { ... on PullRequest { ` + filesSelection + ` } }
  ` + rateLimitFields + `
}`

type textCommentNode struct {
	Author    *loginNode `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
}

func textComments(nodes []textCommentNode) []TextComment {
	var out []TextComment
	for _, n := range nodes {
		out = append(out, TextComment{Author: login(n.Author), Body: n.Body, At: n.CreatedAt})
	}
	return out
}

type filesPage struct {
	PageInfo pageInfo `json:"pageInfo"`
	Nodes    []struct {
		Path string `json:"path"`
	} `json:"nodes"`
}

func (p filesPage) paths() []string {
	var out []string
	for _, n := range p.Nodes {
		out = append(out, n.Path)
	}
	return out
}

type reviewContextNode struct {
	Body     string `json:"body"`
	Comments struct {
		Nodes []textCommentNode `json:"nodes"`
	} `json:"comments"`
	ReviewThreads struct {
		Nodes []struct {
			ID         string `json:"id"`
			IsResolved bool   `json:"isResolved"`
			Path       string `json:"path"`
			Line       *int   `json:"line"`
			Comments   struct {
				Nodes []textCommentNode `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Files filesPage `json:"files"`
}

func (c *client) ReviewContext(ctx context.Context, id string) (PRContext, RateLimit, error) {
	var total RateLimit
	raw, _, rl, err := c.do(ctx, reviewContextQuery, map[string]any{"id": id, "after": nil}, nil)
	if err != nil {
		return PRContext{}, total, err
	}
	total.add(rl)
	var n reviewContextNode
	if err := json.Unmarshal(raw["node"], &n); err != nil {
		return PRContext{}, total, err
	}
	pc := PRContext{Body: n.Body, Comments: textComments(n.Comments.Nodes), Files: n.Files.paths()}
	for _, t := range n.ReviewThreads.Nodes {
		th := Thread{ID: t.ID, Path: t.Path, Resolved: t.IsResolved, Comments: textComments(t.Comments.Nodes)}
		if t.Line != nil {
			th.Line = *t.Line
		}
		pc.Threads = append(pc.Threads, th)
	}
	next := n.Files.PageInfo
	for page := 1; page < contextMaxFilePages && next.HasNextPage; page++ {
		raw, _, rl, err := c.do(ctx, reviewFilesPageQuery, map[string]any{"id": id, "after": next.EndCursor}, nil)
		if err != nil {
			return PRContext{}, total, err
		}
		total.add(rl)
		var more struct {
			Files filesPage `json:"files"`
		}
		if err := json.Unmarshal(raw["node"], &more); err != nil {
			return PRContext{}, total, err
		}
		pc.Files = append(pc.Files, more.Files.paths()...)
		next = more.Files.PageInfo
	}
	return pc, total, nil
}
