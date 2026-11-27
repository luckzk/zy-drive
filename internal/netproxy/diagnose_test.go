// SPDX-License-Identifier: MPL-2.0
package netproxy

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

type timeoutError struct{ msg string }

func (e timeoutError) Error() string   { return e.msg }
func (e timeoutError) Timeout() bool   { return true }
func (e timeoutError) Temporary() bool { return true }

type refusedError struct{ msg string }

func (e refusedError) Error() string   { return e.msg }
func (e refusedError) Timeout() bool   { return false }
func (e refusedError) Temporary() bool { return true }

// IsTimeout must catch every shape a timed-out request can arrive
// in, so TestProxy can say 超时 instead of leaking the raw error.
func TestIsTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context deadline", context.DeadlineExceeded, true},
		{"net timeout error", timeoutError{"i/o timeout"}, true},
		{"client timeout wrapped", &url.Error{Op: "Get", URL: "http://x/", Err: errors.New("Client.Timeout exceeded while awaiting headers")}, true},
		{"deadline in message", errors.New("context deadline exceeded"), true},
		{"refused is not a timeout", refusedError{"connection refused"}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := IsTimeout(c.err); got != c.want {
			t.Errorf("%s: IsTimeout = %v, want %v", c.name, got, c.want)
		}
	}
}
