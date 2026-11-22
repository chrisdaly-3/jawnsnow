package config

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// normalize turns empty slices and maps into nil so that `ignore_checks = []`
// compares equal to an unset field.
func normalize(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			normalize(v.Elem())
		}
	case reflect.Struct:
		for _, field := range v.Fields() {
			if field.CanSet() {
				normalize(field)
			}
		}
	case reflect.Slice:
		if v.Len() == 0 {
			v.Set(reflect.Zero(v.Type()))
			return
		}
		for i := 0; i < v.Len(); i++ {
			normalize(v.Index(i))
		}
	case reflect.Map:
		if v.Len() == 0 {
			v.Set(reflect.Zero(v.Type()))
			return
		}
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			normalize(e)
			v.SetMapIndex(k, e)
		}
	}
}

// The shipped file and the code defaults can never drift apart.
func TestDefaultTOMLMatchesDefaults(t *testing.T) {
	var got Config
	md, err := toml.Decode(string(DefaultTOML()), &got)
	if err != nil {
		t.Fatal(err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		t.Fatalf("unknown keys in default.toml: %v", u)
	}
	want := Defaults()
	normalize(reflect.ValueOf(&got).Elem())
	normalize(reflect.ValueOf(&want).Elem())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default.toml != Defaults():\n%+v\n%+v", got, want)
	}
}

var durationType = reflect.TypeFor[Duration]()

// collectTags gathers every toml tag reachable from t, recursing into
// structs, slice elements and map values.
func collectTags(t reflect.Type, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == durationType {
		return
	}
	for field := range t.Fields() {
		tag := field.Tag.Get("toml")
		if tag == "" {
			continue
		}
		out[tag] = true
		collectTags(field.Type, out)
	}
}

// Every key the Config struct can hold is named in default.toml (as an
// assignment or in a comment).
func TestDefaultTOMLNamesEveryKey(t *testing.T) {
	tags := map[string]bool{}
	collectTags(reflect.TypeFor[Config](), tags)
	if len(tags) < 30 {
		t.Fatalf("collected only %d tags; reflection walk is broken", len(tags))
	}
	text := string(DefaultTOML())
	for tag := range tags {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(tag) + `\b`).MatchString(text) {
			t.Errorf("key %q is not named in default.toml", tag)
		}
	}
}

var assignRE = regexp.MustCompile(`^\s*#?\s*[A-Za-z_][A-Za-z0-9_]*\s*=`)

// Every live or commented-out assignment ends in a one-line comment after its
// value. The [theme.status] rows are the exception: they are wide, so the
// comment above the table header explains them.
func TestDefaultTOMLEveryAssignmentCommented(t *testing.T) {
	lines := strings.Split(string(DefaultTOML()), "\n")
	isAssign := func(s string) bool { return assignRE.MatchString(s) }
	isProse := func(s string) bool {
		s = strings.TrimSpace(s)
		return strings.HasPrefix(s, "#") && !isAssign(s)
	}
	// value is the assignment with any leading "#" removed, from the "=" on.
	value := func(s string) string {
		s = strings.TrimPrefix(strings.TrimSpace(s), "#")
		return s[strings.Index(s, "="):]
	}
	table, tableProse := "", false
	n := 0
	for i, l := range lines {
		if h := strings.TrimSpace(l); strings.HasPrefix(h, "[") {
			table = h
			tableProse = i > 0 && isProse(lines[i-1])
			continue
		}
		if !isAssign(l) {
			continue
		}
		n++
		if table == "[theme.status]" && tableProse {
			continue
		}
		if !strings.Contains(value(l), " # ") {
			t.Errorf("line %d has no comment after its value: %q", i+1, l)
		}
	}
	if n < 40 {
		t.Fatalf("found only %d assignments; the scan is broken", n)
	}
}

// The markdown style is documented as a commented assignment under a [theme]
// table, and the file still parses.
func TestDefaultTOMLDocumentsMarkdown(t *testing.T) {
	text := string(DefaultTOML())
	i := strings.Index(text, "\n[theme]\n")
	if i < 0 {
		t.Fatal("default.toml has no [theme] table")
	}
	j := strings.Index(text[i:], `# markdown = "dark"`)
	k := strings.Index(text, "[theme.status]")
	if j < 0 || i+j > k {
		t.Fatal(`default.toml lacks a commented # markdown = "dark" between [theme] and [theme.status]`)
	}
	var cfg Config
	if _, err := toml.Decode(text, &cfg); err != nil {
		t.Fatalf("default.toml does not parse: %v", err)
	}
}
