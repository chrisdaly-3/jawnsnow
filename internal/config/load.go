package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Path returns the config file snow would use now: the first of
// $XDG_CONFIG_HOME/jawnsnow/config.toml and $HOME/.config/jawnsnow/config.toml
// that exists, or the first-run location when neither does. See Resolve.
func Path() string {
	path, _, _ := Resolve(os.Getenv, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	return path
}

// Resolve picks the config file. It looks at
// $XDG_CONFIG_HOME/jawnsnow/config.toml first, when XDG_CONFIG_HOME is set
// to an absolute path, then at $HOME/.config/jawnsnow/config.toml. found is
// false when neither exists, and path is then where to create the default:
// the XDG location when it's valid, else the home one. ignoredXDG is the
// XDG_CONFIG_HOME value that was skipped for not being absolute, which the
// XDG spec says to ignore.
func Resolve(getenv func(string) string, exists func(string) bool) (path string, found bool, ignoredXDG string) {
	var candidates []string
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		if filepath.IsAbs(x) {
			candidates = append(candidates, filepath.Join(x, "jawnsnow", "config.toml"))
		} else {
			ignoredXDG = x
		}
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		candidates = append(candidates, filepath.Join(".config", "jawnsnow", "config.toml"))
	} else {
		candidates = append(candidates, filepath.Join(home, ".config", "jawnsnow", "config.toml"))
	}
	for _, c := range candidates {
		if exists(c) {
			return c, true, ignoredXDG
		}
	}
	return candidates[0], false, ignoredXDG
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
