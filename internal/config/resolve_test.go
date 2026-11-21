package config

import "slices"

import "testing"

func TestResolve(t *testing.T) {
	const (
		xdg     = "/x/jawnsnow/config.toml"
		homeCfg = "/h/.config/jawnsnow/config.toml"
	)
	tests := []struct {
		name      string
		env       map[string]string
		files     []string
		wantPath  string
		wantFound bool
		wantSkip  string
	}{
		{"xdg file exists", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, []string{xdg}, xdg, true, ""},
		{"falls back to home file", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, []string{homeCfg}, homeCfg, true, ""},
		{"both exist, xdg wins", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, []string{xdg, homeCfg}, xdg, true, ""},
		{"neither, xdg valid", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, nil, xdg, false, ""},
		{"neither, xdg unset", map[string]string{"HOME": "/h"}, nil, homeCfg, false, ""},
		{"relative xdg ignored", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "cfg"}, nil, homeCfg, false, "cfg"},
		{"relative xdg ignored, home file", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "cfg"}, []string{homeCfg}, homeCfg, true, "cfg"},
		{"empty xdg is unset", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": ""}, nil, homeCfg, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists := func(p string) bool {
				return slices.Contains(tt.files, p)
			}
			path, found, skipped := Resolve(func(k string) string { return tt.env[k] }, exists)
			if path != tt.wantPath || found != tt.wantFound || skipped != tt.wantSkip {
				t.Errorf("Resolve = (%q, %v, %q), want (%q, %v, %q)", path, found, skipped, tt.wantPath, tt.wantFound, tt.wantSkip)
			}
		})
	}
}
