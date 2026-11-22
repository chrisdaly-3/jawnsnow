package tui

// itemSpan is where one selectable item sits in the drawer's lines: a
// finding, a check or an activity entry. Its lines run from start to end;
// the ones from start to head are filled when it is selected (a finding's
// header, a check's row, an entry's lines).
type itemSpan struct{ start, head, end int }

// scrollMargin is how many lines j and k keep between the selected item
// and the drawer's top or bottom edge, so it doesn't land against either.
const scrollMargin = 3

// marginFor is scrollMargin, shrunk for a short view so it doesn't fight
// the room there is.
func marginFor(bodyH int) int { return min(scrollMargin, max(bodyH-1, 0)/4) }

// scrollToItem is the scroll that brings s into a view bodyH lines high
// with scrollMargin lines of room above and below, moving from scroll as
// little as it can. An item too tall for the room is shown from its top.
// The caller clamps the result to the drawer's content.
func scrollToItem(scroll int, s itemSpan, bodyH int) int {
	margin := marginFor(bodyH)
	switch {
	case s.end-s.start > bodyH-2*margin, s.start-margin < scroll:
		return max(s.start-margin, 0)
	case s.end+margin > scroll+bodyH:
		return s.end + margin - bodyH
	}
	return scroll
}

// stepSelection is j (dir 1) or k (dir -1) on a tab with spans: it moves
// the selection one item and scrolls as an after-effect, so the item comes
// into view with its margin. The view scrolls a line instead, keeping the
// selection, where moving on would skip something: the rest of a selected
// item taller than the view, the content past the last item (on the Review
// tab, Understanding, Assumptions and Check notes), or the header above the
// first. With no items, j and k scroll. maxScroll is the largest scroll the
// content allows; the caller clamps the result.
func stepSelection(sel, scroll int, spans []itemSpan, bodyH, maxScroll, dir int) (int, int) {
	if len(spans) == 0 {
		return sel, scroll + dir
	}
	sel = min(max(sel, 0), len(spans)-1)
	s, margin := spans[sel], marginFor(bodyH)
	onScreen := s.start < scroll+bodyH && s.end > scroll
	if dir > 0 {
		rest := onScreen && s.end+margin > scroll+bodyH && scroll < maxScroll
		if rest || sel == len(spans)-1 {
			return sel, scroll + 1
		}
		return sel + 1, scrollToItem(scroll, spans[sel+1], bodyH)
	}
	above := onScreen && s.start-margin < scroll && scroll > 0
	if above || sel == 0 {
		return sel, scroll - 1
	}
	return sel - 1, scrollToItem(scroll, spans[sel-1], bodyH)
}

// keyDir is 1 for j or down and -1 for k or up.
func keyDir(k string) int {
	if k == "k" || k == "up" {
		return -1
	}
	return 1
}

// selected is the open tab's selected item, as an index into n spans.
func (m drawerModel) selected(n int) int {
	i := m.sel
	switch m.tab {
	case tabChecks:
		i = m.checkSel
	case tabActivity:
		i = m.actSel
	}
	return min(max(i, 0), n-1)
}
