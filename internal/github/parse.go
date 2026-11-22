package github

import (
	"encoding/json"
	"slices"
	"time"
)

type loginNode struct {
	Typename string `json:"__typename"` // asked for only where a bot matters
	Login    string `json:"login"`
}

// A deleted user ("ghost") arrives as a null author.
func login(a *loginNode) string {
	if a == nil {
		return ""
	}
	return a.Login
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type contextNode struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
	IsRequired bool   `json:"isRequired"`
	DetailsURL string `json:"detailsUrl"`
	TargetURL  string `json:"targetUrl"`
}

type contextsPage struct {
	PageInfo pageInfo      `json:"pageInfo"`
	Nodes    []contextNode `json:"nodes"`
}

type rollupCommit struct {
	Commit struct {
		StatusCheckRollup *struct {
			Contexts contextsPage `json:"contexts"`
		} `json:"statusCheckRollup"`
	} `json:"commit"`
}

type reviewsPage struct {
	Nodes []struct {
		Author      *loginNode `json:"author"`
		State       string     `json:"state"`
		Body        string     `json:"body"`
		SubmittedAt time.Time  `json:"submittedAt"`
		Commit      *struct {
			OID string `json:"oid"`
		} `json:"commit"`
		Comments *struct {
			Nodes []struct {
				ReplyTo *struct {
					ID string `json:"id"`
				} `json:"replyTo"`
			} `json:"nodes"`
		} `json:"comments"`
	} `json:"nodes"`
}

func (p reviewsPage) reviews() []Review {
	var out []Review
	for _, r := range p.Nodes {
		rev := Review{Author: login(r.Author), Bot: r.Author != nil && r.Author.Typename == "Bot", State: r.State, Body: r.Body, SubmittedAt: r.SubmittedAt}
		if r.Commit != nil {
			rev.CommitOID = r.Commit.OID
		}
		if r.Comments != nil {
			for _, c := range r.Comments.Nodes {
				if c.ReplyTo == nil {
					rev.NewThreads++
				}
			}
		}
		out = append(out, rev)
	}
	return out
}

type prNode struct {
	ID             string     `json:"id"`
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	State          PRState    `json:"state"`
	IsDraft        bool       `json:"isDraft"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	MergedAt       time.Time  `json:"mergedAt"`
	ClosedAt       time.Time  `json:"closedAt"`
	Additions      int        `json:"additions"`
	Deletions      int        `json:"deletions"`
	BaseRefName    string     `json:"baseRefName"`
	HeadRefOid     string     `json:"headRefOid"`
	ReviewDecision string     `json:"reviewDecision"`
	Author         *loginNode `json:"author"`
	Repository     struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Head struct {
		Nodes []struct {
			Commit struct {
				OID               string    `json:"oid"`
				CommittedDate     time.Time `json:"committedDate"`
				StatusCheckRollup *struct {
					State    string       `json:"state"`
					Contexts contextsPage `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"head"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID     string `json:"oid"`
				Parents struct {
					Nodes []struct {
						OID string `json:"oid"`
					} `json:"nodes"`
				} `json:"parents"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews  reviewsPage `json:"reviews"`
	Mine     reviewsPage `json:"mine"`
	Comments struct {
		Nodes []struct {
			Author    *loginNode `json:"author"`
			CreatedAt time.Time  `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
}

// parsePR decodes one pull request node into a Snapshot.
func parsePR(raw json.RawMessage) (Snapshot, error) {
	s, _, err := parsePRPage(raw)
	return s, err
}

// parsePRPage also returns the pagination state of the head commit's check
// contexts, so the caller can fetch the rest.
func parsePRPage(raw json.RawMessage) (Snapshot, pageInfo, error) {
	var n prNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return Snapshot{}, pageInfo{}, err
	}
	s := Snapshot{
		ID: n.ID, Repo: n.Repository.NameWithOwner, Number: n.Number,
		Title: n.Title, Author: login(n.Author), URL: n.URL,
		State: n.State, IsDraft: n.IsDraft, HeadOID: n.HeadRefOid, BaseRef: n.BaseRefName,
		ReviewDecision: n.ReviewDecision, Additions: n.Additions, Deletions: n.Deletions,
		UpdatedAt: n.UpdatedAt, MergedAt: n.MergedAt, ClosedAt: n.ClosedAt,
	}
	for _, l := range n.Labels.Nodes {
		s.Labels = append(s.Labels, l.Name)
	}
	for _, c := range n.Commits.Nodes {
		commit := Commit{OID: c.Commit.OID}
		for _, p := range c.Commit.Parents.Nodes {
			commit.Parents = append(commit.Parents, p.OID)
		}
		s.Commits = append(s.Commits, commit)
	}
	// The viewer's reviews usually appear in both lists.
	s.Reviews = n.Reviews.reviews()
	for _, r := range n.Mine.reviews() {
		// Only "mine" fetches comments, so when it repeats a review from the
		// last 50 the kept copy takes its thread count.
		if i := slices.IndexFunc(s.Reviews, r.same); i >= 0 {
			s.Reviews[i].NewThreads = r.NewThreads
		} else {
			s.Reviews = append(s.Reviews, r)
		}
	}
	slices.SortStableFunc(s.Reviews, func(a, b Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	for _, c := range n.Comments.Nodes {
		s.Comments = append(s.Comments, Comment{Author: login(c.Author), At: c.CreatedAt})
	}
	var next pageInfo
	if len(n.Head.Nodes) > 0 {
		head := n.Head.Nodes[0].Commit
		s.HeadCommittedAt = head.CommittedDate
		if head.StatusCheckRollup != nil {
			s.RollupState = head.StatusCheckRollup.State
			s.Checks = checksFrom(head.StatusCheckRollup.Contexts.Nodes)
			next = head.StatusCheckRollup.Contexts.PageInfo
		}
	}
	return s, next, nil
}

type fingerprintNode struct {
	ID         string    `json:"id"`
	UpdatedAt  time.Time `json:"updatedAt"`
	HeadRefOid string    `json:"headRefOid"`
	Commits    struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// fingerprint is the node's Fingerprint, and false for a node that isn't a
// pull request (a null, or a search hit of another type).
func (n *fingerprintNode) fingerprint() (Fingerprint, bool) {
	if n == nil || n.ID == "" {
		return Fingerprint{}, false
	}
	f := Fingerprint{ID: n.ID, UpdatedAt: n.UpdatedAt, HeadOID: n.HeadRefOid}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		f.RollupState = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	return f, true
}

// parseChecksPage decodes the follow-up query's page of check contexts.
func parseChecksPage(raw json.RawMessage) ([]Check, pageInfo, error) {
	var n struct {
		Commits struct {
			Nodes []rollupCommit `json:"nodes"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, pageInfo{}, err
	}
	if len(n.Commits.Nodes) == 0 || n.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return nil, pageInfo{}, nil
	}
	c := n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts
	return checksFrom(c.Nodes), c.PageInfo, nil
}

func checksFrom(nodes []contextNode) []Check {
	var out []Check
	for _, n := range nodes {
		switch n.Typename {
		case "CheckRun":
			out = append(out, Check{Name: n.Name, State: checkRunState(n.Status, n.Conclusion), Required: n.IsRequired, URL: n.DetailsURL})
		case "StatusContext":
			out = append(out, Check{Name: n.Context, State: statusContextState(n.State), Required: n.IsRequired, URL: n.TargetURL})
		}
	}
	return out
}

func checkRunState(status, conclusion string) CheckState {
	if status != "COMPLETED" {
		return CheckPending
	}
	switch conclusion {
	case "SUCCESS":
		return CheckSuccess
	case "NEUTRAL":
		return CheckNeutral
	case "SKIPPED":
		return CheckSkipped
	case "ACTION_REQUIRED":
		return CheckActionRequired
	}
	return CheckFailure
}

func statusContextState(state string) CheckState {
	switch state {
	case "SUCCESS":
		return CheckSuccess
	case "PENDING", "EXPECTED":
		return CheckPending
	}
	return CheckFailure
}

type rateLimitNode struct {
	Remaining int       `json:"remaining"`
	Cost      int       `json:"cost"`
	ResetAt   time.Time `json:"resetAt"`
}

// splitRateLimit separates the rateLimit key from the rest of a response.
func splitRateLimit(raw map[string]json.RawMessage) (RateLimit, error) {
	var rl rateLimitNode
	if b, ok := raw["rateLimit"]; ok {
		if err := json.Unmarshal(b, &rl); err != nil {
			return RateLimit{}, err
		}
	}
	return RateLimit(rl), nil
}
