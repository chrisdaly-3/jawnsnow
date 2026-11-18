// Package config holds configuration types and parsing for snow.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses a Go duration string extended with a leading day
// count, such as "14d" or "1d12h". Negative durations are rejected.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var days time.Duration
	rest := s
	if i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }); i > 0 && s[i] == 'd' {
		n, err := strconv.ParseUint(s[:i], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		days = time.Duration(n) * 24 * time.Hour
		rest = s[i+1:]
		if rest == "" {
			return days, nil
		}
	}
	d, err := time.ParseDuration(rest)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration %q: negative", s)
	}
	return days + d, nil
}

// Duration is a time.Duration that reads and writes the day-aware text form.
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler. It writes whole days as "d"
// and the remainder in the shortest time.Duration form.
func (d Duration) MarshalText() ([]byte, error) {
	const day = 24 * time.Hour
	if d.Duration < day {
		return []byte(shorten(d.Duration)), nil
	}
	out := strconv.FormatInt(int64(d.Duration/day), 10) + "d"
	if rem := d.Duration % day; rem != 0 {
		out += shorten(rem)
	}
	return []byte(out), nil
}

// shorten renders d without the zero-valued trailing units that
// time.Duration.String adds ("5m0s" becomes "5m").
func shorten(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}
