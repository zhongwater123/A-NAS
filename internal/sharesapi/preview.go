package sharesapi

import (
	"bytes"
	"context"
	"errors"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

const maxPreviewSource = 64 << 20

func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	reader, entry, err := h.store.OpenContent(r.Context(), actor(r), r.PathValue("shareID"), r.PathValue("entryID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer reader.Close()
	extension := strings.ToLower(path.Ext(entry.Path))
	if extension != ".heic" && extension != ".heif" {
		reader.Close()
		h.content(w, r)
		return
	}
	if h.media == nil {
		WriteError(w, 503, "share_preview_unavailable", "image preview decoder is unavailable")
		return
	}
	if entry.Size > maxPreviewSource {
		WriteError(w, 413, "share_preview_too_large", "HEIC preview source exceeds 64 MiB")
		return
	}
	select {
	case h.previewSlots <- struct{}{}:
		defer func() { <-h.previewSlots }()
	default:
		h.fail(w, r, shares.ErrBusy)
		return
	}
	seeker, ok := reader.(io.Seeker)
	if !ok {
		h.fail(w, r, errors.New("guarded source seeking unavailable"))
		return
	}
	file, err := anonymousInput(io.LimitReader(reader, maxPreviewSource+1))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer file.Close()
	workerCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if guarded, ok := reader.(interface{ Done() <-chan struct{} }); ok {
		go func() {
			select {
			case <-guarded.Done():
				cancel()
			case <-workerCtx.Done():
			}
		}()
	}
	data, decodeErr := h.media.Image(workerCtx, file, 1600)
	if _, err = seeker.Seek(0, io.SeekCurrent); err != nil {
		h.fail(w, r, err)
		return
	}
	if decodeErr != nil {
		if errors.Is(decodeErr, mediaworker.ErrUnreadable) {
			WriteError(w, 422, "share_preview_unreadable", "image cannot be previewed")
		} else {
			WriteError(w, 503, "share_preview_unavailable", "image preview decoder is unavailable")
		}
		return
	}
	if len(data) > 16<<20 || !bytes.HasPrefix(data, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		WriteError(w, 422, "share_preview_unreadable", "image decoder returned invalid output")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, "preview.png", time.Time{}, &guardedPreview{data: bytes.NewReader(data), guard: seeker})
}

type guardedPreview struct {
	data  *bytes.Reader
	guard io.Seeker
}

func (p *guardedPreview) Read(b []byte) (int, error) {
	if _, err := p.guard.Seek(0, io.SeekCurrent); err != nil {
		return 0, err
	}
	return p.data.Read(b)
}
func (p *guardedPreview) Seek(offset int64, whence int) (int64, error) {
	if _, err := p.guard.Seek(0, io.SeekCurrent); err != nil {
		return 0, err
	}
	return p.data.Seek(offset, whence)
}
