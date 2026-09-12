package aws

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
)

func TestNeedsReauth(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ErrLoginRequired", ErrLoginRequired, true},
		{"wrapped ErrLoginRequired", fmt.Errorf("resolve role: %w", ErrLoginRequired), true},
		{"STS ExpiredToken", &smithy.GenericAPIError{Code: "ExpiredToken", Message: "token expired"}, true},
		{"OIDC ExpiredTokenException", &smithy.GenericAPIError{Code: "ExpiredTokenException"}, true},
		{"SSO UnauthorizedException", &smithy.GenericAPIError{Code: "UnauthorizedException"}, true},
		{"SSO ForbiddenException", &smithy.GenericAPIError{Code: "ForbiddenException"}, true},
		{"string fallback ExpiredToken", errors.New("operation error SSM: StartSession, ExpiredToken: ..."), true},
		{"string fallback UnauthorizedException", errors.New("operation error SSO: ListAccounts, UnauthorizedException: ..."), true},
		{"string fallback ForbiddenException", errors.New("operation error SSO: ListAccounts, ForbiddenException: ..."), true},

		// The near misses that must NOT loop into a browser: a fresh sign-in as the same
		// identity does not grant a permission it never had.
		{"AccessDenied", &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}, false},
		{"UnauthorizedOperation", &smithy.GenericAPIError{Code: "UnauthorizedOperation"}, false},
		{"throttling", &smithy.GenericAPIError{Code: "ThrottlingException"}, false},
		{"plain network error", errors.New("dial tcp: lookup sso.eu-west-2.amazonaws.com: no such host"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsReauth(tc.err); got != tc.want {
				t.Errorf("NeedsReauth(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
