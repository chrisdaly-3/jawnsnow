package poll

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// IsRateLimited reports whether err is GitHub refusing a call for rate
// limiting: a GraphQL RATE_LIMITED error, an HTTP 429, or an HTTP 403 that
// is a primary or secondary rate limit rather than a permission problem.
func IsRateLimited(err error) bool {
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) {
		for _, e := range gqlErr.Errors {
			if e.Type == "RATE_LIMITED" {
				return true
			}
		}
		return false
	}
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return httpErr.Headers.Get("X-Ratelimit-Remaining") == "0" ||
			httpErr.Headers.Get("Retry-After") != "" ||
			strings.Contains(strings.ToLower(httpErr.Message), "rate limit")
	}
	return false
}

// RateLimitReset is when a rate-limited call may be retried, from the
// Retry-After or X-RateLimit-Reset header. It is zero when err carries
// neither, as GraphQL errors never do.
func RateLimitReset(err error, now time.Time) time.Time {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return time.Time{}
	}
	if s := httpErr.Headers.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil {
			return now.Add(time.Duration(secs) * time.Second)
		}
	}
	if s := httpErr.Headers.Get("X-Ratelimit-Reset"); s != "" {
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(unix, 0)
		}
	}
	return time.Time{}
}
