//go:build !linux

package photos

import "os"

// memoryFile returns content as an unlinked temporary file read from its
// start, where in-memory files are not available.
func memoryFile(content []byte) (*os.File, error) {
	file, err := os.CreateTemp("", "anas-ai-input-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(file.Name())
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
