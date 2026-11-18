// Package prompt holds the reviewer's prompt files, adapted from Dispatch
// (MIT, see LICENSE-dispatch).
package prompt

import (
	"bytes"
	_ "embed"
	"encoding/json"
)

//go:embed reviewer.md
var reviewer string

//go:embed scope-full.md
var scopeFull string

//go:embed scope-pr-only.md
var scopePROnly string

//go:embed schema.json
var schema []byte

// Reviewer returns the system prompt.
func Reviewer() string { return reviewer }

// ScopeFull returns the scope text for a review that reads the whole codebase.
func ScopeFull() string { return scopeFull }

// ScopePROnly returns the scope text for a review limited to the PR.
func ScopePROnly() string { return scopePROnly }

// Schema returns schema.json compacted, so it can be passed as a single
// --json-schema argument.
func Schema() string {
	var b bytes.Buffer
	if err := json.Compact(&b, schema); err != nil {
		panic("prompt: embedded schema.json is not valid JSON: " + err.Error())
	}
	return b.String()
}
