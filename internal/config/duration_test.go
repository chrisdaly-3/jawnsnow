package config

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"14d": 14 * 24 * time.Hour, "36h": 36 * time.Hour, "5m": 5 * time.Minute,
		"1d12h": 36 * time.Hour, "90s": 90 * time.Second,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "-1d", "1w", "abc", "-5m"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) should fail", bad)
		}
	}
}

func TestDurationText(t *testing.T) {
	for in, want := range map[string]string{
		"14d": "14d", "36h": "1d12h", "5m": "5m", "90s": "1m30s", "0s": "0s", "24h": "1d",
	} {
		var d Duration
		if err := d.UnmarshalText([]byte(in)); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", in, err)
		}
		got, err := d.MarshalText()
		if err != nil || string(got) != want {
			t.Errorf("MarshalText(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	var d Duration
	if err := d.UnmarshalText([]byte("nope")); err == nil {
		t.Error("UnmarshalText(\"nope\") should fail")
	}
}
