package transfers

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// maxDownloadEntries matches what one archive may select.
const maxDownloadEntries = 1000

// skippedListName lists, inside an archive, the files that could not be read.
const skippedListName = "未能读取的文件.txt"

// errPartial marks a range that was sent whole but stops before the end of
// the file, so the download is not complete yet.
var errPartial = errors.New("partial range")

// writeIdleTimeout cuts a download whose browser took nothing for this long,
// such as one whose network went away without closing the connection.
const writeIdleTimeout = 60 * time.Second

// CreateDownload prepares a download of entries the owner may read: one file
// as itself, a folder or several entries as one ZIP archive. The browser then
// fetches its ContentURL as an ordinary download.
func (s *Service) CreateDownload(ctx context.Context, owner accounts.User, entryIDs []string) (Transfer, error) {
	entryIDs = slices.Compact(slices.Clone(entryIDs))
	if len(entryIDs) == 0 || len(entryIDs) > maxDownloadEntries {
		return Transfer{}, ErrInvalid
	}
	first, _, err := s.files.Lookup(ctx, owner, entryIDs[0])
	if err != nil {
		return Transfer{}, err
	}
	t := Transfer{ID: s.newID(Download), Direction: Download, Kind: KindFile, Name: first.Name, TotalBytes: first.SizeBytes, State: StatePending, entries: entryIDs}
	if len(entryIDs) > 1 || first.Kind == files.EntryKindDirectory {
		// Listing the archive now checks every entry and gives its size.
		items, err := s.files.ArchiveItems(ctx, owner, entryIDs)
		if err != nil {
			return Transfer{}, err
		}
		t.Kind, t.Name, t.TotalBytes = KindArchive, archiveName(first.Name, len(entryIDs)), archiveSize(items)
	}
	if t.Location, err = s.location(ctx, owner, first.SpaceID, first.ParentID); err != nil {
		return Transfer{}, err
	}
	t.CreatedAt, t.UpdatedAt = s.now(), s.now()
	if err := s.store.save(ctx, owner.ID, t); err != nil {
		return Transfer{}, err
	}
	t.decorate()
	return t, nil
}

// ServeDownload sends a download's content and follows it. started reports
// whether the response began: before that the caller answers err as an API
// error, after it a failure must cut the connection, so that a truncated
// response is never taken for a complete one. A single file honours range
// requests, so a browser resumes it where the connection dropped, also while
// the dropped connection lingers, and download managers may fetch parts of
// it in parallel.
func (s *Service) ServeDownload(w http.ResponseWriter, r *http.Request, owner accounts.User, id string) (started bool, err error) {
	if r.Method != http.MethodGet {
		return false, ErrInvalid
	}
	t, err := s.store.get(r.Context(), owner.ID, id)
	if err != nil || t.Direction != Download {
		return false, ErrNotFound
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// A failed, canceled or finished download starts again when its browser
	// resumes it or fetches it anew.
	live, request := s.joinDownload(owner, t, cancel)
	writer := &countingWriter{ResponseWriter: w, idle: http.NewResponseController(w), report: func(start, bytes int64) { s.downloadProgress(live, start, bytes) }}
	var failure error
	if t.Kind == KindArchive {
		failure = s.serveArchive(ctx, writer, owner, live, t)
	} else {
		failure = s.serveFile(ctx, writer, r.WithContext(ctx), owner, live, t)
	}
	cut := writer.failed != nil || ctx.Err() != nil
	last, canceled := s.leaveDownload(live, request)
	switch {
	case canceled:
		// Cancel already saved the transfer.
		return writer.started, ErrCanceled
	case !last:
		// Another request still sends the file; the last one settles it.
		if failure != nil && !errors.Is(failure, errPartial) {
			return writer.started, failure
		}
		return true, nil
	}
	s.mu.Lock()
	if failure == nil && t.Kind == KindArchive {
		live.transfer.TransferredBytes = live.transfer.TotalBytes
	}
	whole := live.transfer.TransferredBytes >= live.transfer.TotalBytes
	s.mu.Unlock()
	switch {
	case (failure == nil || errors.Is(failure, errPartial)) && whole:
		s.settle(ctx, live, StateCompleted, nil)
		return true, nil
	case failure == nil || errors.Is(failure, errPartial):
		// The browser asked for part of the file; it asks for the rest later.
		s.settle(ctx, live, StateInterrupted, nil)
		return true, nil
	case cut:
		// The browser went away. The download is over unless the browser
		// resumes it, which starts it again.
		s.settle(ctx, live, StateFailed, FailureOf(ErrInterrupted))
		return writer.started, failure
	default:
		s.settle(ctx, live, StateFailed, FailureOf(failure))
		return writer.started, failure
	}
}

func (s *Service) serveFile(ctx context.Context, w *countingWriter, r *http.Request, owner accounts.User, live *liveTransfer, t Transfer) error {
	content, err := s.files.OpenContent(ctx, owner, t.entries[0])
	if err != nil {
		return err
	}
	defer content.Reader.Close()
	modified := content.ModifiedAt
	if info, err := content.Reader.Stat(); err == nil {
		s.mu.Lock()
		live.transfer.TotalBytes = info.Size()
		s.mu.Unlock()
		// The file as it is now, not as the catalog last saw it: a browser
		// resuming a file changed since, over SMB perhaps, gets it whole
		// rather than the old start joined to the new end.
		modified = info.ModTime()
		w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().UnixNano()))
	}
	// An attachment needs no sniffed type; the browser saves it as it is.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": content.Name}))
	http.ServeContent(w, r, content.Name, modified, &cancelableFile{ReadSeeker: content.Reader, ctx: ctx})
	if w.status != http.StatusOK && w.status != http.StatusPartialContent {
		return fmt.Errorf("%w: download answered %d", ErrState, w.status)
	}
	if w.failed != nil {
		return w.failed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.written != w.length {
		return io.ErrUnexpectedEOF
	}
	s.mu.Lock()
	size := live.transfer.TotalBytes
	s.mu.Unlock()
	if w.start+w.length != size {
		return errPartial
	}
	return nil
}

// serveArchive streams a ZIP archive of the transfer's entries, listed again
// now because they may have changed since the download was created. Files
// are stored, not compressed: most large files on a NAS are compressed
// already, and storing keeps the NAS CPU out of the way. Files that cannot
// be opened are left out and listed in a text file inside the archive.
func (s *Service) serveArchive(ctx context.Context, w *countingWriter, owner accounts.User, live *liveTransfer, t Transfer) error {
	items, err := s.files.ArchiveItems(ctx, owner, t.entries)
	if err != nil {
		return err
	}
	s.mu.Lock()
	live.transfer.TotalBytes = archiveSize(items)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": t.Name}))
	w.WriteHeader(http.StatusOK)
	w.counting = false
	archive := zip.NewWriter(w)
	var copied int64
	var skipped []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := &zip.FileHeader{Name: item.Path, Method: zip.Store, Modified: item.ModTime}
		if item.Kind == files.EntryKindDirectory {
			header.Name += "/"
			if _, err := archive.CreateHeader(header); err != nil {
				return err
			}
			continue
		}
		file, err := s.files.OpenArchiveItem(ctx, item)
		if err != nil {
			skipped = append(skipped, item.Path)
			continue
		}
		entry, err := archive.CreateHeader(header)
		if err == nil {
			var count int64
			count, err = io.Copy(entry, &cancelableReader{reader: file, ctx: ctx, report: func(bytes int64) { s.progress(live, copied+bytes) }})
			copied += count
		}
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	if len(skipped) > 0 {
		s.mu.Lock()
		live.transfer.Skipped = len(skipped)
		s.mu.Unlock()
		list, err := archive.CreateHeader(&zip.FileHeader{Name: skippedListName, Method: zip.Deflate, Modified: s.now()})
		if err != nil {
			return err
		}
		if _, err := io.WriteString(list, "以下文件在打包时无法读取，未包含在压缩包中：\r\n\r\n"+strings.Join(skipped, "\r\n")+"\r\n"); err != nil {
			return err
		}
	}
	return archive.Close()
}

func archiveName(first string, count int) string {
	if count == 1 {
		return first + ".zip"
	}
	return fmt.Sprintf("%s 等 %d 项.zip", first, count)
}

func archiveSize(items []files.ArchiveItem) int64 {
	var total int64
	for _, item := range items {
		total += item.Size
	}
	return total
}

// countingWriter follows a download response: its status, the range it
// sends and how much of it was written. An archive counts the bytes of the
// files it reads instead. Each write must be taken within writeIdleTimeout.
type countingWriter struct {
	http.ResponseWriter
	idle *http.ResponseController
	// report gives the start of the range and the bytes just written.
	report   func(start, bytes int64)
	status   int
	started  bool
	counting bool
	// start and length are the range the response sends.
	start, length, written int64
	failed                 error
}

func (w *countingWriter) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started, w.status, w.counting = true, status, true
	header := w.Header()
	w.length, _ = strconv.ParseInt(header.Get("Content-Length"), 10, 64)
	if status == http.StatusPartialContent {
		// Content-Range: bytes start-end/size
		if value, ok := strings.CutPrefix(header.Get("Content-Range"), "bytes "); ok {
			first, _, _ := strings.Cut(value, "-")
			w.start, _ = strconv.ParseInt(first, 10, 64)
		}
	}
	w.ResponseWriter.WriteHeader(status)
	if w.counting {
		w.report(w.start, 0)
	}
}

func (w *countingWriter) Write(data []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if w.idle != nil {
		// Unsupported only where tests write to a recorder.
		_ = w.idle.SetWriteDeadline(time.Now().Add(writeIdleTimeout))
	}
	count, err := w.ResponseWriter.Write(data)
	if w.counting {
		w.written += int64(count)
		w.report(w.start, int64(count))
	}
	if err != nil && w.failed == nil {
		w.failed = err
	}
	return count, err
}

func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// cancelableFile stops a download's reads once it is canceled.
type cancelableFile struct {
	io.ReadSeeker
	ctx context.Context
}

func (f *cancelableFile) Read(buffer []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.ReadSeeker.Read(buffer)
}

// cancelableReader reads one archive file and reports the bytes read.
type cancelableReader struct {
	reader io.Reader
	ctx    context.Context
	read   int64
	report func(int64)
}

func (r *cancelableReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.Read(buffer)
	if count > 0 {
		r.read += int64(count)
		r.report(r.read)
	}
	return count, err
}
