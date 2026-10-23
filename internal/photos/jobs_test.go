package photos

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/imagetest"
)

func TestRenderJobTurnsAPanicIntoAPermanentFailure(t *testing.T) {
	// Any panic on the render path, here from a service without a store,
	// must fail the job instead of taking the photo service down.
	panics := imagetest.LoggedPanics(t)
	var s Service
	thumbnail, _, _, class := s.renderJob(context.Background(), claimedJob{objectID: strings.Repeat("a", 64)})
	if thumbnail != nil || class != jobErrorUndecodable {
		t.Fatalf("renderJob() = %d bytes, class %q; want no thumbnail and %q", len(thumbnail), class, jobErrorUndecodable)
	}
	// The log tells this bug in the photo service from a corrupt original.
	logged := panics()
	if len(logged) != 1 || logged[0].Decoder != "photo thumbnail" || logged[0].Level != slog.LevelError ||
		!strings.Contains(logged[0].Value, "nil pointer") ||
		!slices.ContainsFunc(logged[0].Stack, func(frame string) bool {
			return strings.HasPrefix(frame, "github.com/zhongwater123/A-NAS/internal/photos.(*Service).openObject:")
		}) {
		t.Fatalf("logged %+v, want the nil pointer reached from openObject", logged)
	}
}

func TestAnUploadWhoseHeaderPanicsIsUnsupported(t *testing.T) {
	// Here the panic comes from a service without a store, before the
	// header is read; one in image.DecodeConfig is handled the same way.
	panics := imagetest.LoggedPanics(t)
	var s Service
	if _, err := s.readMetadata(stagedObject{name: "staged", mediaType: "image/png"}); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("readMetadata() error = %v, want ErrUnsupportedType", err)
	}
	if logged := panics(); len(logged) != 1 || logged[0].Decoder != "photo import" || !strings.Contains(logged[0].Value, "nil pointer") {
		t.Fatalf("logged %+v, want the photo import's nil pointer", logged)
	}
}
