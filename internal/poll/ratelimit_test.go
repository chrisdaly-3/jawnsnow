package poll

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

func httpErr(status int, msg string, headers map[string]string) *api.HTTPError {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &api.HTTPError{StatusCode: status, Message: msg, Headers: h}
}

func TestIsRateLimited(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"other error", io.EOF, false},
		{"graphql RATE_LIMITED", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED", Message: "API rate limit exceeded"}}}, true},
		{"graphql other type", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve"}}}, false},
		{"http 429", httpErr(429, "Too Many Requests", nil), true},
		{"http 403 remaining 0", httpErr(403, "Forbidden", map[string]string{"X-Ratelimit-Remaining": "0"}), true},
		{"http 403 retry-after", httpErr(403, "Forbidden", map[string]string{"Retry-After": "60"}), true},
		{"http 403 secondary limit message", httpErr(403, "You have exceeded a secondary rate limit", nil), true},
		{"http 403 permission", httpErr(403, "Resource not accessible by integration", nil), false},
		{"http 500", httpErr(500, "Internal Server Error", nil), false},
		{"wrapped", fmt.Errorf("search feed %q: %w", "A", httpErr(429, "", nil)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRateLimited(tc.err); got != tc.want {
				t.Errorf("IsRateLimited = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRateLimitReset(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	reset := now.Add(10 * time.Minute)
	tests := []struct {
		name string
		err  error
		want time.Time
	}{
		{"retry-after seconds", httpErr(403, "secondary rate limit", map[string]string{"Retry-After": "90"}), now.Add(90 * time.Second)},
		{"x-ratelimit-reset", httpErr(403, "", map[string]string{"X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": strconv.FormatInt(reset.Unix(), 10)}), reset},
		{"graphql has no reset", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED"}}}, time.Time{}},
		{"bad header", httpErr(429, "", map[string]string{"Retry-After": "soon"}), time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RateLimitReset(tc.err, now); !got.Equal(tc.want) {
				t.Errorf("RateLimitReset = %v, want %v", got, tc.want)
			}
		})
	}
}
