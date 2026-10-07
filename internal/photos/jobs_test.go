package photos

import (
	"strings"
	"testing"
)

func TestRenderJobTurnsAPanicIntoAPermanentFailure(t *testing.T) {
	// Any panic on the render path, here from a service without a store,
	// must fail the job instead of taking the photo service down.
	var s Service
	thumbnail, _, _, class := s.renderJob(claimedJob{objectID: strings.Repeat("a", 64)})
	if thumbnail != nil || class != jobErrorUndecodable {
		t.Fatalf("renderJob() = %d bytes, class %q; want no thumbnail and %q", len(thumbnail), class, jobErrorUndecodable)
	}
}
