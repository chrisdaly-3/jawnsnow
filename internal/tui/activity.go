package tui

import (
	"context"
	"maps"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// activityTimeout bounds one Activity fetch, a single small query.
const activityTimeout = 30 * time.Second

// activityState is one PR's timeline for the drawer's Activity tab (plan 5),
// kept in memory for the session (G1).
type activityState struct {
	UpdatedAt   time.Time // the PR's updatedAt the items were fetched for
	Items       []github.ActivityItem
	Loaded      bool // Items holds a fetch's answer
	Loading     bool
	Err         string // the last fetch's error, shown until the next one
	RateLimited bool   // no fetch started: GitHub's limit is spent (G2)
	seq         int    // the newest fetch asked for
}

// activityMsg is an Activity fetch's answer.
type activityMsg struct {
	PRID      string
	Seq       int
	UpdatedAt time.Time
	Items     []github.ActivityItem
	Rate      github.RateLimit
	Err       error
}

// fetchActivity starts a fetch of the PR's timeline in the background,
// unless the cache already has it at the PR's current updatedAt or one is
// on its way. While GitHub's rate limit is spent it starts none (G2).
func (m Model) fetchActivity(id string) (Model, tea.Cmd) {
	t, ok := m.prs[id]
	if !ok || m.client == nil {
		return m, nil
	}
	cur := m.activity[id]
	if cur.Loading || (cur.Loaded && cur.Err == "" && cur.UpdatedAt.Equal(t.PR.UpdatedAt)) {
		return m, nil
	}
	m.activity = cloneMap(m.activity)
	if m.quota.Limited() {
		cur.RateLimited = true
		m.activity[id] = cur
		return m, nil
	}
	m.activitySeq++
	cur.seq, cur.Loading, cur.RateLimited = m.activitySeq, true, false
	m.activity[id] = cur
	client, seq, at := m.client, cur.seq, t.PR.UpdatedAt
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), activityTimeout)
		defer cancel()
		items, rl, err := client.Activity(ctx, id)
		return activityMsg{PRID: id, Seq: seq, UpdatedAt: at, Items: items, Rate: rl, Err: err}
	}
}

// applyActivity takes a fetch's answer. An answer older than the newest
// fetch asked for that PR is dropped. Answers for a PR the drawer has moved
// away from are kept, so going back costs nothing.
func (m Model) applyActivity(msg activityMsg) (Model, tea.Cmd) {
	cur, ok := m.activity[msg.PRID]
	if !ok || msg.Seq != cur.seq {
		return m, nil
	}
	m.quota.Observe(msg.Rate)
	cur.Loading = false
	switch {
	case github.IsRateLimited(msg.Err):
		cur.RateLimited, cur.Err = true, ""
	case msg.Err != nil:
		cur.Err = msg.Err.Error()
	default:
		cur.Items, cur.UpdatedAt, cur.Loaded, cur.Err = msg.Items, msg.UpdatedAt, true, ""
	}
	m.activity = maps.Clone(m.activity)
	m.activity[msg.PRID] = cur
	// A poll may have moved the PR on while this fetch ran.
	if m.drawer.isOpen() && m.drawer.prID == msg.PRID && cur.Loaded && cur.Err == "" {
		return m.refreshActivity()
	}
	return m, nil
}

// refreshActivity fetches the open drawer's timeline again when its PR has
// changed since the last fetch (G1). A failed or rate-limited fetch waits
// for the next open instead (G2).
func (m Model) refreshActivity() (Model, tea.Cmd) {
	if !m.drawer.isOpen() {
		return m, nil
	}
	id := m.drawer.prID
	cur, t := m.activity[id], m.prs[id]
	if !cur.Loaded || cur.Err != "" || cur.RateLimited || cur.UpdatedAt.Equal(t.PR.UpdatedAt) {
		return m, nil
	}
	return m.fetchActivity(id)
}
