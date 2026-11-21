package tui

// itemSpan is where one selectable item sits in the drawer's lines: a
// finding, a check or an activity entry. Its lines run from start to end;
// the ones from start to head are filled when it is selected (a finding's
// header, a check's row, an entry's lines).
type itemSpan struct{ start, head, end int }

// scrollMargin is how many lines n and N keep between the selected item
// and the drawer's top or bottom edge, so it doesn't land against either.
const scrollMargin = 3

// scrollToItem is the scroll that brings s into a view bodyH lines high
// with scrollMargin lines of room above and below, moving from scroll as
// little as it can. An item too tall for the room is shown from its top.
// The caller clamps the result to the drawer's content.
func scrollToItem(scroll int, s itemSpan, bodyH int) int {
	margin := min(scrollMargin, max(bodyH-1, 0)/4)
	switch {
	case s.end-s.start > bodyH-2*margin, s.start-margin < scroll:
		return max(s.start-margin, 0)
	case s.end+margin > scroll+bodyH:
		return s.end + margin - bodyH
	}
	return scroll
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
