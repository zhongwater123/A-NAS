package photos

import (
	"io"
	"os"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

func memoryFile(content []byte) (*os.File, error) {
	return sealedFile("anas-ai-input", func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
}

// sealedFile returns what write writes as a sealed in-memory file read from
// its start, as memoryFile does, for content too large to hold twice.
func sealedFile(name string, write func(io.Writer) error) (*os.File, error) {
	return unixmsg.WriteSealedFile(name, write)
}
