package tui

import "maps"

// cloneMap copies m so a Model value can change it without touching the
// copy an earlier value holds. A nil map comes back empty, ready to write.
func cloneMap[M ~map[K]V, K comparable, V any](m M) M {
	if m == nil {
		return M{}
	}
	return maps.Clone(m)
}
