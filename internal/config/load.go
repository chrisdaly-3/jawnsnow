package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Path returns the config file location, $HOME/.config/jawnsnow/config.toml.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "jawnsnow", "config.toml")
	}
	return filepath.Join(home, ".config", "jawnsnow", "config.toml")
}

// Load reads the config file at path over Defaults(). A missing file returns
// Defaults() and an error wrapping os.ErrNotExist. Unknown keys come back as
// warnings.
func Load(path string) (cfg Config, warnings []string, err error) {
	cfg = Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return Defaults(), nil, fmt.Errorf("read config: %w", err)
	}
	// A [[feeds]] list replaces the default feed rather than adding to it.
	var fileOnly struct {
		Feeds []Feed `toml:"feeds"`
	}
	if _, err := toml.Decode(string(data), &fileOnly); err != nil {
		return Defaults(), nil, fmt.Errorf("parse %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Defaults(), nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if md.IsDefined("feeds") {
		cfg.Feeds = fileOnly.Feeds
	}
	for i := range cfg.Feeds {
		if cfg.Feeds[i].Since.Duration == 0 {
			cfg.Feeds[i].Since = Duration{DefaultSince}
		}
	}
	// scope defaults to "full"; map values are copies, so write them back.
	for name, r := range cfg.Repos {
		if r.Scope == "" {
			r.Scope = "full"
			cfg.Repos[name] = r
		}
	}
	for _, k := range md.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("unknown key %q", k.String()))
	}
	return cfg, warnings, nil
}
