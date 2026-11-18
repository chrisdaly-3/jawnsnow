package config

import _ "embed"

//go:embed default.toml
var defaultTOML []byte

// DefaultTOML returns the fully commented default config shipped in the
// binary. It decodes to exactly Defaults().
func DefaultTOML() []byte {
	return append([]byte(nil), defaultTOML...)
}
