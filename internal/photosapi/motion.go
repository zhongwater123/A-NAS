package photosapi

import (
	"archive/zip"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// The paired endpoint streams the still then its MOV directly into managed
// staging. No original is buffered in RAM or spilled onto the system disk.
func (h *handler) uploadLive(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*photos.DefaultMaxImportBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		WriteError(w, 400, "invalid_multipart", "请选择照片与同名 MOV")
		return
	}
	var directoryID string
	for {
		part, err := reader.NextPart()
		if err != nil {
			WriteError(w, 400, "invalid_multipart", "请选择照片与同名 MOV")
			return
		}
		if part.FormName() == "directoryId" {
			value, err := io.ReadAll(io.LimitReader(part, 4097))
			part.Close()
			if err != nil || len(value) > 4096 {
				WriteError(w, 400, "invalid_multipart", "目录无效")
				return
			}
			directoryID = string(value)
			continue
		}
		if part.FormName() != "file" {
			part.Close()
			WriteError(w, 400, "invalid_multipart", "请先提交照片，再提交同名 MOV")
			return
		}
		name := part.FileName()
		motionName := strings.TrimSuffix(name, filepath.Ext(name)) + ".MOV"
		principal, _ := principalFrom(r)
		asset, err := h.service.Import(r.Context(), principal, photos.ImportRequest{LibraryID: r.PathValue("libraryID"), DirectoryID: directoryID, Name: name, Content: part, MotionName: motionName, MotionContent: &pairedPartReader{reader: reader, name: motionName}})
		part.Close()
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, asset)
		return
	}
}

// uploadLiveVideo takes a Live Photo's MOV uploaded without its still and
// completes the photos of the library that were waiting for it.
func (h *handler) uploadLiveVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, photos.DefaultMaxImportBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
		return
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		principal, _ := principalFrom(r)
		assets, err := h.service.AttachLiveVideo(r.Context(), principal, photos.LiveVideoRequest{
			LibraryID: r.PathValue("libraryID"), Name: part.FileName(), Content: part,
		})
		_ = part.Close()
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, itemsResponse[photos.Asset]{Items: assets})
		return
	}
	WriteError(w, http.StatusBadRequest, "file_required", "one file part is required")
}

type pairedPartReader struct {
	reader *multipart.Reader
	part   *multipart.Part
	name   string
	done   bool
}

func (r *pairedPartReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	if r.part == nil {
		part, err := r.reader.NextPart()
		if err != nil {
			return 0, photos.ErrInvalidMotion
		}
		if part.FormName() != "motion" || !strings.EqualFold(part.FileName(), r.name) {
			return 0, photos.ErrInvalidMotion
		}
		r.part = part
	}
	n, err := r.part.Read(p)
	if errors.Is(err, io.EOF) {
		r.done = true
		if _, nextErr := r.reader.NextPart(); !errors.Is(nextErr, io.EOF) {
			return n, photos.ErrInvalidMotion
		}
	}
	return n, err
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	content, err := h.service.Preview(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serve(w, r, content, "inline")
}
func (h *handler) motion(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	content, err := h.service.OpenMotion(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serve(w, r, content, "inline")
}
func (h *handler) playMotion(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	stream, err := h.service.PlayMotion(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, stream)
}

// Android's original already contains both parts; Live Photo exports a ZIP
// containing the untouched still and MOV under a matching basename.
func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	id := r.PathValue("assetID")
	asset, err := h.service.Get(r.Context(), principal, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	photo, err := h.service.Open(r.Context(), principal, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if asset.Motion == nil || asset.Motion.Kind != "live-photo" {
		h.serve(w, r, photo, "attachment")
		return
	}
	defer photo.Reader.Close()
	motion, err := h.service.OpenMotion(r.Context(), principal, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer motion.Reader.Close()
	base := strings.TrimSuffix(photo.Name, filepath.Ext(photo.Name))
	if base == "" {
		base = "photo"
	}
	// A virtual rename may remove or change the extension. Export valid
	// companion names so the pair can be selected and imported again.
	extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/heic": ".heic"}[photo.MediaType]
	photoName := base + extension
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": base + ".zip"}))
	archive := zip.NewWriter(w)
	for _, part := range []struct {
		name   string
		reader io.Reader
	}{{photoName, photo.Reader}, {base + ".MOV", motion.Reader}} {
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: part.name, Method: zip.Store})
		if err != nil {
			return
		}
		if _, err = io.Copy(writer, part.reader); err != nil {
			return
		}
	}
	_ = archive.Close()
}
