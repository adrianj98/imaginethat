package drive

import (
	"errors"
	"testing"

	"github.com/adrianm/imagethat/core/xfer"
)

func TestHTTPErrorRefusals(t *testing.T) {
	cases := []struct {
		status  int
		message string
		refused bool
	}{
		{400, "bad request", true},
		{401, "invalid credentials", true},
		{403, "rate limit exceeded", false},
		{408, "timeout", false},
		{429, "slow down", false},
		{503, "backend error", false},
	}
	for _, c := range cases {
		err := &HTTPError{Status: c.status, Message: c.message}
		if got := errors.Is(err, xfer.ErrRefused); got != c.refused {
			t.Errorf("%d %q: refused = %v, want %v", c.status, c.message, got, c.refused)
		}
	}
}
