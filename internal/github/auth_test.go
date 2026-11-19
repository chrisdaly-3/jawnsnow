package github

import (
	"fmt"
	"io"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
)

func TestIsUnauthorized(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"other error", io.EOF, false},
		{"http 401", httpErr(401, "Bad credentials", nil), true},
		{"wrapped 401", fmt.Errorf("look up the gh user: %w", httpErr(401, "Bad credentials", nil)), true},
		{"http 403", httpErr(403, "Forbidden", nil), false},
		{"http 429", httpErr(429, "Too Many Requests", nil), false},
		{"graphql error", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve"}}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnauthorized(tc.err); got != tc.want {
				t.Errorf("IsUnauthorized = %v, want %v", got, tc.want)
			}
		})
	}
}
