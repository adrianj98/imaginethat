package remote

import (
	"errors"
	"net/http"
	"testing"

	"github.com/adrianm/imagethat/core/xfer"
)

func TestStatusRefusals(t *testing.T) {
	for code, refused := range map[int]bool{400: true, 413: true, 408: false, 429: false, 500: false, 503: false} {
		err := status(&http.Response{StatusCode: code}, "read", "x")
		if got := errors.Is(err, xfer.ErrRefused); got != refused {
			t.Errorf("HTTP %d: refused = %v, want %v", code, got, refused)
		}
	}
}
