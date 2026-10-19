package shares

import (
	"context"
	"errors"
	"io"
	"os"
)

func captureFile(ctx context.Context, dst *os.File, src io.Reader, expected int64, requireClone bool) (int64, error) {
	if source, ok := src.(*os.File); ok {
		if err := cloneFile(dst, source); err == nil {
			info, err := dst.Stat()
			if err != nil {
				return 0, err
			}
			return info.Size(), ctx.Err()
		} else if requireClone {
			return 0, err
		}
	} else if requireClone {
		return 0, errors.New("stable source file descriptor required")
	}
	n, err := io.Copy(dst, &contextReader{ctx: ctx, reader: io.LimitReader(src, expected+1)})
	return n, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
