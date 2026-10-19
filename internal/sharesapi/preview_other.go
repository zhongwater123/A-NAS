//go:build !linux

package sharesapi

import (
	"errors"
	"io"
	"os"
)

func anonymousInput(io.Reader) (*os.File, error) {
	return nil, errors.New("anonymous image preview input requires Linux")
}
