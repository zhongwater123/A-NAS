package photos

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// The object detector reads a larger image than the 512 px thumbnail that
// image vectors use (docs/architecture/photo-objects.md): small things vanish
// at 512 px, and the detector squares its input, so the short side needs
// room. The input is rendered from the original for each job and handed to
// the Worker as a sealed in-memory file; it is never written to disk.
const (
	aiInputEdge    = 1600
	aiInputQuality = 92
)

// detectionInput is a claimed object's enlarged input with the background
// jobs it serves: a detection job, a face scan or a pet scan, with whichever
// of the others the same object has due, which then share one rendering of
// the original (docs/architecture/photo-people.md#输入图). Without an input
// it says why: the jobs' failure class, or a storage fault.
type detectionInput struct {
	job    claimedJob
	others []claimedJob
	file   *os.File
	class  string
	err    error
}

// jobs lists the input's claimed jobs.
func (input *detectionInput) jobs() []claimedJob {
	return append([]claimedJob{input.job}, input.others...)
}

// wantedScans says which background jobs the Worker's models can serve.
type wantedScans struct{ detecting, facing, petting bool }

func (w wantedScans) derivations() []string {
	var derivations []string
	for _, wanted := range []struct {
		on         bool
		derivation string
	}{{w.detecting, detectionDerivation}, {w.facing, facesDerivation}, {w.petting, petsDerivation}} {
		if wanted.on {
			derivations = append(derivations, wanted.derivation)
		}
	}
	return derivations
}

// claimDetection claims the next due detection job, or else face scan, or
// else pet scan, with the same object's other wanted jobs that are due, and
// renders the input; with none due it returns sql.ErrNoRows. A detection
// comes first, so that a pet scan claimed with it reads its dogs.
func (s *Service) claimDetection(ctx context.Context, wanted wantedScans) (*detectionInput, error) {
	derivations := wanted.derivations()
	var job claimedJob
	err := sql.ErrNoRows
	for _, derivation := range derivations {
		if job, err = s.claimJob(ctx, derivation); !errors.Is(err, sql.ErrNoRows) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	input := &detectionInput{job: job}
	for _, derivation := range derivations {
		if derivation == job.derivation {
			continue
		}
		other, err := s.claimObjectJob(ctx, derivation, job.objectID)
		switch {
		case err == nil:
			input.others = append(input.others, other)
		case !errors.Is(err, sql.ErrNoRows):
			return nil, errors.Join(err, s.returnDetection(ctx, input))
		}
	}
	input.file, input.class, input.err = s.openAIInput(ctx, job)
	return input, nil
}

// returnDetection gives back claimed jobs that will not run now; a nil input
// is none.
func (s *Service) returnDetection(ctx context.Context, input *detectionInput) error {
	if input == nil {
		return nil
	}
	if input.file != nil {
		_ = input.file.Close()
	}
	var err error
	for _, job := range input.jobs() {
		err = errors.Join(err, s.returnJob(ctx, job))
	}
	return err
}

// openAIInput renders the detector's input for a claimed job. A decoder
// panic counts as an undecodable original, as for thumbnails.
func (s *Service) openAIInput(ctx context.Context, job claimedJob) (file *os.File, class string, err error) {
	defer func() {
		if recover() != nil {
			file, class, err = nil, jobErrorUndecodable, nil
		}
	}()
	original, err := s.openObject(job.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, jobErrorMissingOriginal, nil
	}
	if err != nil {
		return nil, "", err
	}
	defer original.Close()
	content, _, _, err := s.renderPhoto(ctx, original, job.orientation, aiInputEdge, aiInputQuality)
	if err != nil {
		if errors.Is(err, mediaworker.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, jobErrorIO, nil
		}
		return nil, jobErrorUndecodable, nil
	}
	file, err = memoryFile(content)
	return file, "", err
}
