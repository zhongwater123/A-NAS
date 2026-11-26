//go:build linux

package photoservice

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// The library decides what becomes of a job by these three errors: it waits
// for a Worker that is away, spends an attempt on one that failed and gives
// up on a request it refused. A caller's own context is none of them.
func TestTheComputeWorkersErrorsBecomeTheLibrarys(t *testing.T) {
	kinds := []error{photos.ErrComputeUnavailable, photos.ErrComputeFailed, photos.ErrComputeInvalidResult}
	for _, c := range []struct {
		err  error
		want error
	}{
		{fmt.Errorf("%w: connection refused", videocutcompute.ErrUnavailable), photos.ErrComputeUnavailable},
		{fmt.Errorf("%w: internal: the operation failed", videocutcompute.ErrFailed), photos.ErrComputeFailed},
		{fmt.Errorf("%w: unexpected EOF", videocutcompute.ErrFailed), photos.ErrComputeFailed},
		{fmt.Errorf("%w: message is too large", videocutcompute.ErrRefused), photos.ErrComputeInvalidResult},
		{context.Canceled, nil},
		{context.DeadlineExceeded, nil},
	} {
		got := mapped(c.err)
		if !errors.Is(got, c.err) {
			t.Errorf("mapped(%v) = %v, which lost the cause", c.err, got)
		}
		for _, kind := range kinds {
			if errors.Is(got, kind) != errors.Is(c.want, kind) {
				t.Errorf("mapped(%v) = %v, want only %v", c.err, got, c.want)
			}
		}
	}
	if mapped(nil) != nil {
		t.Error("mapped(nil) is an error")
	}
}
