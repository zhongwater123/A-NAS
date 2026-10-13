package photos

import (
	"errors"
	"io/fs"
	"os"
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

// openAIInput renders the detector's input for a claimed job. A decoder
// panic counts as an undecodable original, as for thumbnails.
func (s *Service) openAIInput(job claimedJob) (file *os.File, class string, err error) {
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
	content, _, _, err := renderUpright(original, job.orientation, aiInputEdge, aiInputQuality)
	if err != nil {
		return nil, jobErrorUndecodable, nil
	}
	file, err = memoryFile(content)
	return file, "", err
}
