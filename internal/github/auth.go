package github

import (
	"errors"
	"net/http"

	"github.com/cli/go-gh/v2/pkg/api"
)

// IsUnauthorized reports whether err is GitHub rejecting the login itself:
// an HTTP 401, as for an expired or revoked token. A 403 is a permission or
// rate limit problem, not a bad login.
func IsUnauthorized(err error) bool {
	var httpErr *api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}
