package chart

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

func dollars(v float64) string { return "$" + strconv.FormatFloat(v, 'f', 2, 64) }

func TestRenderDimensions(t *testing.T) {
	lines := []Line{
		{Name: "a", Values: []float64{1, 3, 2, 5, 4}, Color: 39},
		{Name: "b", Values: []float64{0, 1, 0, 2, 1}, Color: 141},
	}
	for _, sz := range [][2]int{{20, 6}, {40, 10}, {80, 24}, {12, 3}, {9, 4}, {5, 3}, {3, 2}, {10, 1}} {
		for _, nc := range []bool{false, true} {
			rows := Render(lines, Options{Width: sz[0], Height: sz[1], Cursor: 2, NoColor: nc, YLabel: dollars})
			if len(rows) != sz[1] {
				t.Fatalf("%dx%d: got %d rows", sz[0], sz[1], len(rows))
			}
			for i, r := range rows {
				if w := lipgloss.Width(r); w > sz[0] {
					t.Errorf("%dx%d row %d width %d > %d: %q", sz[0], sz[1], i, w, sz[0], r)
				}
			}
		}
	}
	if got := Render(lines, Options{Width: 0, Height: 5}); len(got) != 5 {
		t.Errorf("zero width: %d rows", len(got))
	}
	if got := Render(lines, Options{Width: 10, Height: 0}); len(got) != 0 {
		t.Errorf("zero height: %d rows", len(got))
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	got = ansi.Strip(got) + "\n"
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs; run with -update and read it\n--- got ---\n%s", name, got)
	}
}

func TestRenderSingleLineShape(t *testing.T) {
	vals := []float64{0.1, 0.3, 0.5, 0.4, 0.8, 1.0, 1.2, 1.5}
	rows := Render([]Line{{Name: "cost", Values: vals, Color: 39}}, Options{
		Width: 30, Height: 8, Cursor: -1, YLabel: dollars,
		XLabels: []string{"Mon", "Wed", "Fri"}, NoColor: true,
	})
	golden(t, "single_line.golden", strings.Join(rows, "\n"))

	if !strings.Contains(rows[0], "$2.00") || !strings.Contains(rows[5], "$0.00") {
		t.Errorf("axis labels wrong: %q / %q", rows[0], rows[5])
	}
}

func hasDots(rs []rune) bool {
	for _, r := range rs {
		if r > 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

func TestRenderCursor(t *testing.T) {
	l := []Line{{Name: "a", Values: []float64{1, 2, 3, 4}, Color: 39}}
	count := func(c int) int {
		rows := Render(l, Options{Width: 24, Height: 8, Cursor: c, NoColor: true})
		return strings.Count(strings.Join(rows, "\n"), "│")
	}
	if count(1) == 0 {
		t.Error("in-range cursor drew nothing")
	}
	for _, c := range []int{-1, 4, 99} {
		if count(c) != 0 {
			t.Errorf("cursor %d should draw nothing", c)
		}
	}
}

func TestChartDegenerateSeries(t *testing.T) {
	cases := map[string][]Line{
		"nil":    nil,
		"empty":  {{Name: "a", Color: 39}},
		"one":    {{Name: "a", Values: []float64{3}, Color: 39}},
		"zeros":  {{Name: "a", Values: []float64{0, 0, 0}, Color: 39}},
		"spike":  {{Name: "a", Values: []float64{0, 0, 1e12, 0}, Color: 39}},
		"nan":    {{Name: "a", Values: []float64{math.NaN(), math.Inf(1), -3, 2}, Color: 39}},
		"noname": {{Values: []float64{1, 2}, Color: 39}},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			rows := Render(lines, Options{Width: 30, Height: 8, Cursor: 0, YLabel: dollars, XLabels: []string{"a", "b"}})
			if len(rows) != 8 {
				t.Fatalf("rows = %d", len(rows))
			}
			out := strings.Join(rows, "\n")
			for _, bad := range []string{"NaN", "Inf"} {
				if strings.Contains(out, bad) {
					t.Errorf("output contains %s:\n%s", bad, out)
				}
			}
			for _, r := range rows {
				if lipgloss.Width(r) > 30 {
					t.Errorf("row too wide: %q", r)
				}
			}
		})
	}
	// All-zero data draws its flat line on the bottom plot row.
	rows := Render(cases["zeros"], Options{Width: 30, Height: 8, Cursor: -1, NoColor: true})
	if !hasDots([]rune(rows[5])) {
		t.Errorf("flat line not on the bottom row:\n%s", strings.Join(rows, "\n"))
	}
	for _, r := range rows[:5] {
		if hasDots([]rune(r)) {
			t.Errorf("unexpected dots above the bottom row:\n%s", strings.Join(rows, "\n"))
		}
	}
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

func TestRenderColorsAreXterm256(t *testing.T) {
	lines := []Line{
		{Name: "a", Values: []float64{1, 2, 3}, Color: 39},
		{Name: "b", Values: []float64{3, 2, 1}, Color: 214},
	}
	rows := Render(lines, Options{Width: 30, Height: 8, Cursor: 1, XLabels: []string{"x", "y"}})
	out := strings.Join(rows, "\n")
	ms := sgr.FindAllStringSubmatch(out, -1)
	if len(ms) == 0 {
		t.Fatal("no SGR codes emitted")
	}
	seen := map[int]bool{}
	for _, m := range ms {
		switch {
		case m[1] == "0" || m[1] == "39":
		case strings.HasPrefix(m[1], "38;5;"):
			n, err := strconv.Atoi(strings.TrimPrefix(m[1], "38;5;"))
			if err != nil || n < 16 || n > 255 {
				t.Errorf("index out of 16-255: %q", m[0])
			}
			seen[n] = true
		default:
			t.Errorf("unexpected SGR %q", m[0])
		}
	}
	if !seen[39] || !seen[214] {
		t.Errorf("series colors missing: %v", seen)
	}
	plain := Render(lines, Options{Width: 30, Height: 8, Cursor: 1, NoColor: true})
	if strings.Contains(strings.Join(plain, ""), "\x1b") {
		t.Error("NoColor output contains escape codes")
	}
}

func TestRenderLastLineWinsCell(t *testing.T) {
	same := []float64{1, 1, 1}
	rows := Render([]Line{{Name: "a", Values: same, Color: 39}, {Name: "b", Values: same, Color: 214}},
		Options{Width: 20, Height: 6, Cursor: -1})
	plot := strings.Join(rows[:4], "")
	if strings.Contains(plot, "38;5;39m") {
		t.Error("overdrawn cells should take the last line's color")
	}
}

func TestNiceMax(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0, 1}, {-2, 1}, {math.NaN(), 1}, {0.3, 0.5}, {0.9, 1}, {1, 1}, {1.01, 2}, {2, 2}, {3, 5}, {5, 5},
		{5.1, 10}, {7, 10}, {12, 20}, {99, 100}, {100, 100}, {430, 500}, {1e12, 1e12}, {0.0021, 0.005},
	}
	for _, c := range cases {
		if got := niceMax(c.in); math.Abs(got-c.want) > c.want*1e-9 {
			t.Errorf("niceMax(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBrailleBits(t *testing.T) {
	var c cell
	c.set(0, 0)
	c.set(1, 3)
	if r := rune(0x2800) + rune(c.bits); r != '⢁' {
		t.Errorf("got %q", r)
	}
}
