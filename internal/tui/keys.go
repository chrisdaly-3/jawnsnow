package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type keyHint struct{ keys, help string }

type keySection struct {
	title string
	keys  []keyHint
}

// costPickerHelp is the Cost view's filter picker, which only the help for
// that modal lists.
var costPickerHelp = []keyHint{
	{"h l", "previous or next dimension"},
	{"j k", "move through the values"},
	{"space", "include or leave out the value"},
	{"esc", "close the picker"},
}

// keyMap is every key snow uses. The help overlay shows the Everywhere
// section and the current view's (see helpSections), so it fits a terminal.
var keyMap = []keySection{
	{"Everywhere", []keyHint{
		{"[ ]", "previous or next view"},
		{"1-5", "jump to a view"},
		{"tab shift+tab", "move focus"},
		{"R", "poll now"},
		{"m", "toggle watcher or auto mode"},
		{"?", "show or hide help"},
		{"esc", "close help"},
		{"q ctrl+c", "quit"},
	}},
	{"Wall", []keyHint{
		{"h l", "previous or next lane"},
		{"j k", "next or previous card"},
		{"x", "hide"},
		{"enter", "open the review drawer"},
		{"o", "open in the browser"},
		{"space", "review with Claude now"},
		{"c", "review with a model and effort you pick"},
		{"/", "filter"},
		{"a", "filter by author"},
		{"esc", "clear the filters"},
	}},
	{"Drawer", []keyHint{
		{"esc", "close the drawer"},
		{"h l", "Review, Checks or Activity tab"},
		{"j k", "scroll"},
		{"J K", "jump to the bottom or top"},
		{"n N", "next or previous finding, check or entry"},
		{"< >", "older or newer pass"},
		{"o", "open the PR"},
		{"O", "open the finding's line, the check or the entry"},
		{"y", "copy the finding, the check's name or the entry"},
		{"space c", "review again, or pick a model"},
		{"P", "post this review to GitHub, after a preview"},
	}},
	{"Post", []keyHint{
		{"y n", "post again, or not, when it was posted before"},
		{"j k", "choose the event, or the finding"},
		{"tab", "move between the summary box and the findings"},
		{"shift+enter", "a new line in the summary box"},
		{"space", "include or leave out the finding"},
		{"e", "open the summary in $VISUAL or $EDITOR"},
		{"m", "show the raw markdown or the rendered view"},
		{"ctrl+d ctrl+u", "scroll the preview"},
		{"enter", "pick the event, then post"},
		{"esc", "back out a step"},
	}},
	{"Cost filter", costPickerHelp},
	{"Agent", []keyHint{
		{"j k", "choose a tier"},
		{"enter", "pick a model and effort for this session"},
		{"x", "clear the tier's session pick"},
		{"e", "edit config.toml in $VISUAL or $EDITOR"},
	}},
	{"Cost", []keyHint{
		{"r", "cycle the range: 7d, 30d, 90d or custom"},
		{"f", "filter by author, repo, model, effort, tier, mode or outcome"},
		{"g", "group by model, author, repo or tier"},
		{"b", "bucket by day, week or hour"},
		{"t", "running total or per bucket"},
		{"h l", "move the chart cursor"},
		{"j k", "choose a top PR"},
		{"enter", "show the PR in the Reviews view"},
		{"tab", "filters, chart or top PRs"},
	}},
	{"Reviews", []keyHint{
		{"j k", "next or previous review"},
		{"/", "filter by repo, number, title or author"},
		{"o", "cycle the outcome: all, complete, incomplete, failed"},
		{"enter", "open the review drawer at that pass"},
		{"esc", "clear the filters"},
	}},
	{"Done", []keyHint{
		{"j k", "next or previous row"},
		{"x", "unhide"},
		{"enter", "open the review drawer"},
		{"o", "open in the browser"},
	}},
}

func allKeys() []keyHint {
	var out []keyHint
	for _, s := range keyMap {
		out = append(out, s.keys...)
	}
	return out
}

// footerKeys are the short hints for each view, before the global ones.
var footerKeys = map[View][]keyHint{
	ViewWall:    {{"h/l", "lane"}, {"j/k", "card"}, {"space", "review"}, {"x", "hide"}, {"o", "open"}, {"/", "filter"}, {"a", "author"}},
	ViewAgent:   {{"j/k", "tier"}, {"enter", "pick"}, {"x", "clear"}, {"e", "edit config"}},
	ViewDone:    {{"j/k", "row"}, {"x", "unhide"}, {"o", "open"}},
	ViewReviews: {{"/", "filter"}, {"o", "outcome"}, {"enter", "open"}, {"esc", "clear"}},
	ViewCost:    {{"r", "range"}, {"f", "filter"}, {"g", "group"}, {"b", "bucket"}, {"t", "total"}, {"enter", "reviews"}},
}

// The post flow's footer, by stage. m's hint names the view it switches to.
var (
	postLoadingKeys = []keyHint{{"esc", "cancel"}}
	postRepostKeys  = []keyHint{{"y", "post again"}, {"n", "cancel"}}
	postPickKeys    = []keyHint{{"j/k", "event"}, {"enter", "select"}, {"esc", "cancel"}, {"?", "help"}}
	postPostingKeys = []keyHint{{"", "posting to GitHub…"}}
	postResultKeys  = []keyHint{{"esc", "close"}}
	// postHoldOnKey leads the preview's footer after an enter that came
	// too soon to post.
	postHoldOnKey = keyHint{"", "hold on — press enter again to post"}
)

func postPreviewKeys(raw bool) []keyHint {
	m := keyHint{"m", "raw"}
	if raw {
		m.help = "rendered"
	}
	return []keyHint{{"tab", "summary"}, {"j/k", "finding"}, {"space", "toggle"}, {"e", "open in $EDITOR"}, m,
		{"ctrl+d/ctrl+u", "scroll"}, {"enter", "post"}, {"esc", "back"}, {"?", "help"}}
}

// postBoxKeys is the preview's footer while the summary box has the keys.
// enterPosts is whether enter posts there (see postFlow.enterPosts).
func postBoxKeys(enterPosts bool) []keyHint {
	if enterPosts {
		return []keyHint{{"shift+enter", "new line"}, {"enter", "post"}, {"tab/esc", "leave the box"}}
	}
	return []keyHint{{"enter", "new line"}, {"tab/esc", "leave the box"}}
}

var footerGlobal = []keyHint{{"[ ]", "views"}, {"R", "poll"}, {"m", "mode"}, {"?", "help"}, {"q", "quit"}}

// The Cost view's footer while its range input or filter picker has the
// keys.
var (
	costRangeKeys  = []keyHint{{"enter", "apply"}, {"esc", "cancel"}}
	costPickerKeys = []keyHint{{"h/l", "dimension"}, {"j/k", "value"}, {"space", "toggle"}, {"esc", "close"}, {"?", "help"}}
)

// The Reviews view's footer while its filter input has the keys.
var reviewLogInputKeys = []keyHint{{"enter", "apply"}, {"esc", "cancel"}}

func hintParts(hints []keyHint) []string {
	parts := make([]string, len(hints))
	for i, k := range hints {
		parts[i] = strings.TrimSpace(k.keys + " " + k.help)
	}
	return parts
}

// hintLine joins the view's hints and the global ones into one line of at
// most width cells. The global hints always end it: view hints that don't
// fit are dropped whole from the right, leaving "…" in their place, and
// only the globals alone are ever cut short.
func hintLine(view, global []keyHint, width int) string {
	vp, gp := hintParts(view), hintParts(global)
	for k := len(vp); ; k-- {
		parts := slices.Clone(vp[:k])
		if k < len(vp) {
			parts = append(parts, "…")
		}
		line := " " + strings.Join(append(parts, gp...), " · ")
		if k == 0 || lipgloss.Width(line) <= width {
			return ansi.Truncate(line, max(width, 0), "…")
		}
	}
}

var viewSection = map[View]string{
	ViewWall: "Wall", ViewAgent: "Agent", ViewDone: "Done", ViewReviews: "Reviews", ViewCost: "Cost",
}

// helpSections are the sections the help overlay shows: Everywhere, then
// the open modal's or the current view's, then the review drawer's when it
// is open in this view.
func (m Model) helpSections() []keySection {
	titles := []string{"Everywhere"}
	switch {
	case m.post != nil:
		titles = append(titles, "Post")
	case m.view == ViewCost && m.cost.picking:
		titles = append(titles, "Cost filter")
	default:
		titles = append(titles, viewSection[m.view])
		if m.drawerHere() {
			titles = append(titles, "Drawer")
		}
	}
	var out []keySection
	for _, t := range titles {
		for _, s := range keyMap {
			if s.title == t {
				out = append(out, s)
			}
		}
	}
	return out
}

func (m Model) helpBody(width, height int) string {
	secs := m.helpSections()
	keyW := 0
	for _, s := range secs {
		for _, k := range s.keys {
			keyW = max(keyW, lipgloss.Width(k.keys))
		}
	}
	var b strings.Builder
	for i, s := range secs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.theme.Accent().Render(s.title))
		for _, k := range s.keys {
			b.WriteString("\n  ")
			b.WriteString(m.theme.Accent().Render(k.keys))
			b.WriteString(strings.Repeat(" ", keyW-lipgloss.Width(k.keys)+2))
			b.WriteString(k.help)
		}
	}
	box := m.theme.Border(true).Padding(0, 2).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
