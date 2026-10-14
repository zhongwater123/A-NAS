package transfersapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/transfers"
)

// uploadStore is tusd's data store for transfers.Service. tusd keeps the
// request context's values, so every call runs as the signed-in user with
// the session token the File Broker checks. The metadata a client sends:
//
//	filename      the file's name
//	spaceId       the space to upload to
//	parentId      the folder in it; empty for the space root
//	relativePath  folders to create below it, "/"-separated (folder uploads)
//	conflict      ask (default), rename or replace; see files.ConflictPolicy
//	lastModified  the file's modification time in milliseconds
//	batch         groups the uploads one action started
type uploadStore struct {
	service *transfers.Service
	logger  *slog.Logger
}

func (s uploadStore) NewUpload(ctx context.Context, info tusd.FileInfo) (tusd.Upload, error) {
	user, ok := userOf(ctx)
	if !ok {
		return nil, uploadError(http.StatusUnauthorized, "authentication_required", "authentication is required")
	}
	if info.SizeIsDeferred || info.IsPartial || info.IsFinal {
		return nil, uploadError(http.StatusBadRequest, "invalid_request", "uploads must declare their length")
	}
	meta := info.MetaData
	conflict := files.ConflictPolicy(meta["conflict"])
	if conflict == "" {
		conflict = files.ConflictAsk
	}
	var directories []string
	if relative := strings.Trim(meta["relativePath"], "/"); relative != "" {
		directories = strings.Split(relative, "/")
	}
	lastModified, _ := strconv.ParseInt(meta["lastModified"], 10, 64)
	t, err := s.service.BeginUpload(ctx, user, transfers.UploadRequest{
		UploadRequest: files.UploadRequest{
			SpaceID: meta["spaceId"], ParentID: meta["parentId"], Directories: directories,
			Name: meta["filename"], Size: info.Size, Conflict: conflict,
		},
		Batch: meta["batch"], RelativePath: strings.Join(directories, "/"), LastModified: lastModified,
	})
	if err != nil {
		return nil, s.failure(ctx, err)
	}
	return &upload{store: s, user: user, id: t.ID}, nil
}

func (s uploadStore) GetUpload(ctx context.Context, id string) (tusd.Upload, error) {
	user, ok := userOf(ctx)
	if !ok {
		return nil, tusd.ErrNotFound
	}
	if _, err := s.service.UploadStatus(ctx, user, id); err != nil {
		return nil, tusd.ErrNotFound
	}
	return &upload{store: s, user: user, id: id}, nil
}

func (s uploadStore) AsTerminatableUpload(value tusd.Upload) tusd.TerminatableUpload {
	return value.(*upload)
}

type upload struct {
	store uploadStore
	user  accounts.User
	id    string
}

// GetInfo reports a completed upload at its full length, so a client that
// missed the final response does not send it again.
func (u *upload) GetInfo(ctx context.Context) (tusd.FileInfo, error) {
	t, err := u.store.service.UploadStatus(ctx, u.user, u.id)
	if err != nil {
		return tusd.FileInfo{}, tusd.ErrNotFound
	}
	offset := t.TransferredBytes
	switch {
	case t.State == transfers.StateCompleted:
		offset = t.TotalBytes
	case offset == t.TotalBytes:
		// Every byte arrived but saving the file failed, the data volume
		// being offline perhaps: save it now that the browser asks again,
		// rather than tell it the upload is complete.
		if _, err := u.store.service.FinishUpload(ctx, u.user, u.id); err != nil {
			return tusd.FileInfo{}, u.store.failure(ctx, err)
		}
	}
	return tusd.FileInfo{ID: u.id, Size: t.TotalBytes, Offset: offset}, nil
}

func (u *upload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	written, err := u.store.service.WriteUpload(ctx, u.user, u.id, offset, src)
	if err != nil {
		return written, u.store.failure(ctx, err)
	}
	return written, nil
}

func (u *upload) GetReader(context.Context) (io.ReadCloser, error) {
	return nil, tusd.ErrNotImplemented
}

func (u *upload) FinishUpload(ctx context.Context) error {
	if _, err := u.store.service.FinishUpload(ctx, u.user, u.id); err != nil {
		return u.store.failure(ctx, err)
	}
	return nil
}

func (u *upload) Terminate(ctx context.Context) error {
	if _, err := u.store.service.Cancel(ctx, u.user, u.id); err != nil && !errors.Is(err, transfers.ErrNotFound) {
		return u.store.failure(ctx, err)
	}
	return nil
}

// failure answers in the product API's JSON error shape, which clients
// read the same way on every endpoint.
func (s uploadStore) failure(ctx context.Context, err error) error {
	described := transfers.FailureOf(err)
	status := statusOf(described)
	if status == http.StatusInternalServerError {
		s.logger.ErrorContext(ctx, "upload failed", "error", err)
	}
	return uploadError(status, described.Code, described.Message)
}

func uploadError(status int, code, message string) tusd.Error {
	body, _ := json.Marshal(errorBody(code, message))
	return tusd.Error{
		ErrorCode: code, Message: message,
		HTTPResponse: tusd.HTTPResponse{StatusCode: status, Body: string(body), Header: tusd.HTTPHeader{"Content-Type": "application/json"}},
	}
}
