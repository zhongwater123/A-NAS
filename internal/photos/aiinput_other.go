//go:build !linux

package photos

import (
	"bufio"
	"errors"
	"io"
	"os"
)

// memoryFile returns content as an unlinked temporary file read from its
// start, where in-memory files are not available.
func memoryFile(content []byte) (*os.File, error) {
	return sealedFile("anas-ai-input", func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
}

// sealedFile returns what write writes as an unlinked temporary file read
// from its start, where in-memory files are not available.
func sealedFile(name string, write func(io.Writer) error) (*os.File, error) {
	file, err := os.CreateTemp("", name+"-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(file.Name())
	buffered := bufio.NewWriter(file)
	if err := errors.Join(write(buffered), buffered.Flush()); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
