package photosapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// The montage center reaches members' videos through project grants that
// the photo library holds (ADR 0038 decision 3), on a socket only its
// account can connect to. HTTP cannot pass a descriptor, so the protocol is
// unixmsg: one request and one answer per connection, the original's
// descriptor beside the answer to open. These types are both ends'.

// Grant operations.
const (
	GrantRegister = "register"
	GrantLookup   = "lookup"
	GrantOpen     = "open"
	GrantRemove   = "remove"
	GrantList     = "list"
	GrantRevoke   = "revoke"
	// GrantAnalyze queues the montage analysis of granted videos;
	// GrantAnalysis returns one video's, its window vectors in a memfd
	// beside the answer.
	GrantAnalyze  = "analyze"
	GrantAnalysis = "analysis"
	// GrantSaveFilm imports a film, passed as the request's descriptor,
	// into the owner's private library, once per version key.
	GrantSaveFilm = "saveFilm"
)

// Grant error codes. GrantOwnerInactive is a grant whose owner's account is
// not in use now, as while an administrator has disabled it: the grant
// works again once the account does, so the montage center waits rather
// than giving the project up, as it does on GrantNotFound.
const (
	GrantInvalid        = "invalid_request"
	GrantUnauthorized   = "authentication_required"
	GrantNotFound       = "grant_not_found"
	GrantOwnerInactive  = "owner_inactive"
	GrantAssetsRefused  = "assets_not_grantable"
	GrantTooMany        = "too_many_assets"
	GrantAssetNotFound  = "not_found"
	GrantUnavailable    = "photos_unavailable"
	GrantNoSpace        = "insufficient_storage"
	grantMessageLimit   = 256 << 10
	grantRequestTimeout = time.Minute
	SaveFilmTimeout     = 30 * time.Minute
)

// GrantListPage is how many grants one list answer carries at most. A grant
// encodes to under 1 KiB even when every byte of its 128-byte project ID
// needs escaping, so a page stays well inside GrantMessageLimit.
const GrantListPage = 200

// NewGrantPrincipal builds the photo Principal the montage center acts as
// for a member under a project grant: it sees videos and opens originals,
// adds only to the member's private library and changes nothing else, and
// never acts as an administrator or through a viewing grant.
func NewGrantPrincipal(userID, username string) photos.Principal {
	return photos.Principal{UserID: userID, Username: username, Grant: true}
}

// GrantRequest is one request on the grant socket. Session is the member's
// session token, on register only: it is used for that request and not kept.
// After, on list, is the Next of the page before.
type GrantRequest struct {
	Op        string   `json:"op"`
	Session   string   `json:"session,omitempty"`
	ProjectID string   `json:"projectId,omitempty"`
	GrantID   string   `json:"grantId,omitempty"`
	AssetIDs  []string `json:"assetIds,omitempty"`
	AssetID   string   `json:"assetId,omitempty"`
	After     string   `json:"after,omitempty"`
	// Retry, on analyze, starts a failed analysis over.
	Retry bool `json:"retry,omitempty"`
	// VersionKey and Name, on saveFilm.
	VersionKey string `json:"versionKey,omitempty"`
	Name       string `json:"name,omitempty"`
}

// GrantResponse answers a GrantRequest. Next, on list, is set when more
// grants may follow: the next page is asked for after it.
type GrantResponse struct {
	OK       bool                  `json:"ok"`
	Code     string                `json:"code,omitempty"`
	Message  string                `json:"message,omitempty"`
	Grant    *photos.Grant         `json:"grant,omitempty"`
	Assets   []photos.GrantedAsset `json:"assets,omitempty"`
	Grants   []photos.Grant        `json:"grants,omitempty"`
	Next     string                `json:"next,omitempty"`
	ObjectID string                `json:"objectId,omitempty"`
	Analysis *photos.GrantAnalysis `json:"analysis,omitempty"`
	// BulkAnalysis carries the variable-length lists in a sealed descriptor
	// after WindowBytes of binary window vectors and headers.
	BulkAnalysis bool  `json:"bulkAnalysis,omitempty"`
	WindowBytes  int64 `json:"windowBytes,omitempty"`
}

// GrantError is a refused request.
type GrantError struct{ Code, Message string }

func (e *GrantError) Error() string { return "photo grant: " + e.Code + ": " + e.Message }

// GrantErrorCode is err's code, or "" when the request did not reach an
// answer.
func GrantErrorCode(err error) string {
	if refused, ok := errors.AsType[*GrantError](err); ok {
		return refused.Code
	}
	return ""
}

// GrantCall sends one grant request and returns its answer, with the
// descriptor that came with it. A refused request is a *GrantError.
type GrantCall func(ctx context.Context, request GrantRequest) (GrantResponse, *os.File, error)

// Grants are the grant operations over a call: the socket's (GrantClient)
// or, in development, the photo service's own in this process.
type Grants struct {
	Call GrantCall
	// Send is Call with a descriptor beside the request, for saveFilm.
	Send func(ctx context.Context, request GrantRequest, file *os.File) (GrantResponse, *os.File, error)
}

// GrantClient reaches the photo library's grant socket.
func GrantClient(socket string) Grants {
	return Grants{
		Call: func(ctx context.Context, request GrantRequest) (GrantResponse, *os.File, error) {
			return callSocket(ctx, socket, request, nil)
		},
		Send: func(ctx context.Context, request GrantRequest, file *os.File) (GrantResponse, *os.File, error) {
			return callSocket(ctx, socket, request, file)
		},
	}
}

func callSocket(ctx context.Context, socket string, request GrantRequest, file *os.File) (GrantResponse, *os.File, error) {
	timeout := grantRequestTimeout
	if request.Op == GrantSaveFilm {
		timeout = SaveFilmTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return GrantResponse{}, nil, fmt.Errorf("photo grant socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	unix := conn.(*net.UnixConn)
	if err := unixmsg.Write(unix, request, file, grantMessageLimit); err != nil {
		return GrantResponse{}, nil, fmt.Errorf("photo grant request: %w", err)
	}
	var response GrantResponse
	answered, err := unixmsg.Read(unix, &response, grantMessageLimit)
	if err != nil {
		return GrantResponse{}, nil, fmt.Errorf("photo grant answer: %w", err)
	}
	if !response.OK {
		unixmsg.CloseFile(answered)
		return GrantResponse{}, nil, &GrantError{Code: response.Code, Message: response.Message}
	}
	return response, answered, nil
}

func (g Grants) plain(ctx context.Context, request GrantRequest) (GrantResponse, error) {
	response, file, err := g.Call(ctx, request)
	unixmsg.CloseFile(file)
	return response, err
}

// Register registers, or extends, the grant of a member's project with
// assetIDs, as the member signed in with session.
func (g Grants) Register(ctx context.Context, session, projectID string, assetIDs []string) (photos.Grant, error) {
	response, err := g.plain(ctx, GrantRequest{Op: GrantRegister, Session: session, ProjectID: projectID, AssetIDs: assetIDs})
	if err != nil {
		return photos.Grant{}, err
	}
	if response.Grant == nil {
		return photos.Grant{}, errors.New("photo grant: register answered no grant")
	}
	return *response.Grant, nil
}

// Lookup says where each asset of the grant stands.
func (g Grants) Lookup(ctx context.Context, grantID string, assetIDs []string) ([]photos.GrantedAsset, error) {
	response, err := g.plain(ctx, GrantRequest{Op: GrantLookup, GrantID: grantID, AssetIDs: assetIDs})
	return response.Assets, err
}

// Open returns a granted video's original, read-only, and its object.
func (g Grants) Open(ctx context.Context, grantID, assetID string) (*os.File, string, error) {
	response, file, err := g.Call(ctx, GrantRequest{Op: GrantOpen, GrantID: grantID, AssetID: assetID})
	if err != nil {
		return nil, "", err
	}
	if file == nil {
		return nil, "", errors.New("photo grant: open answered no descriptor")
	}
	return file, response.ObjectID, nil
}

// Analyze queues the montage analysis of granted videos; with retry, a
// failed analysis starts over.
func (g Grants) Analyze(ctx context.Context, grantID string, assetIDs []string, retry bool) error {
	_, err := g.plain(ctx, GrantRequest{Op: GrantAnalyze, GrantID: grantID, AssetIDs: assetIDs, Retry: retry})
	return err
}

// Analysis returns a granted video's montage analysis so far and, once its
// windows are done, their List and vectors: WindowCount × Dimensions
// little-endian float32 in window order. They come in a sealed memfd beside
// the answer (photos.GrantAnalysis), so a long video's answer stays within
// the message limit.
func (g Grants) Analysis(ctx context.Context, grantID, assetID string) (photos.GrantAnalysis, []byte, error) {
	response, file, err := g.Call(ctx, GrantRequest{Op: GrantAnalysis, GrantID: grantID, AssetID: assetID})
	if err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	defer unixmsg.CloseFile(file)
	if response.Analysis == nil {
		return photos.GrantAnalysis{}, nil, errors.New("photo grant: analysis answered nothing")
	}
	if response.BulkAnalysis {
		return readBulkAnalysis(response, file)
	}
	analysis := *response.Analysis
	if analysis.Windows != photos.AnalysisDone {
		return analysis, nil, nil
	}
	if file == nil {
		return photos.GrantAnalysis{}, nil, errors.New("photo grant: analysis answered no windows")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	vectors, err := photos.ReadWindows(&analysis, file)
	if err != nil {
		return photos.GrantAnalysis{}, nil, fmt.Errorf("photo grant: %w", err)
	}
	return analysis, vectors, nil
}

// SaveFilm imports film into the grant owner's private library and returns
// its asset; the same versionKey again returns the film saved before.
func (g Grants) SaveFilm(ctx context.Context, grantID, versionKey, name string, film *os.File) (string, error) {
	if g.Send == nil {
		return "", errors.New("photo grant: saving a film needs a descriptor transport")
	}
	response, file, err := g.Send(ctx, GrantRequest{Op: GrantSaveFilm, GrantID: grantID, VersionKey: versionKey, Name: name}, film)
	unixmsg.CloseFile(file)
	if err != nil {
		return "", err
	}
	return response.ObjectID, nil
}

// Bulk analysis keeps the M5 binary windows together and appends the M7
// card metadata. The socket frame carries only the split position and state.
type analysisLists struct {
	Descriptions []photos.CardDescription `json:"descriptions,omitempty"`
}

const maxAnalysisJSON = 256 << 20

// WriteAnalysis streams the sealed windows and appends the variable-length lists.
func WriteAnalysis(w io.Writer, analysis photos.GrantAnalysis, windows *os.File) (GrantResponse, error) {
	var n int64
	if windows != nil {
		info, err := windows.Stat()
		if err != nil {
			return GrantResponse{}, err
		}
		n, err = io.Copy(w, io.NewSectionReader(windows, 0, info.Size()))
		if err != nil {
			return GrantResponse{}, err
		}
	}
	metadata, err := json.Marshal(analysisLists{analysis.Descriptions})
	if err != nil {
		return GrantResponse{}, err
	}
	if len(metadata) > maxAnalysisJSON {
		return GrantResponse{}, errors.New("photo grant: analysis lists exceed limit")
	}
	if _, err = w.Write(metadata); err != nil {
		return GrantResponse{}, err
	}
	analysis.CardList, analysis.Descriptions = nil, nil
	return GrantResponse{Analysis: &analysis, BulkAnalysis: true, WindowBytes: n}, nil
}

func readBulkAnalysis(response GrantResponse, file *os.File) (photos.GrantAnalysis, []byte, error) {
	if file == nil {
		return photos.GrantAnalysis{}, nil, errors.New("photo grant: missing analysis descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	n := response.WindowBytes
	if n < 0 || n >= info.Size() || info.Size()-n > maxAnalysisJSON {
		return photos.GrantAnalysis{}, nil, errors.New("photo grant: invalid analysis descriptor size")
	}
	analysis := *response.Analysis
	var vectors []byte
	if analysis.Windows == photos.AnalysisDone {
		vectors, err = photos.ReadWindows(&analysis, io.NewSectionReader(file, 0, n))
		if err != nil {
			return photos.GrantAnalysis{}, nil, err
		}
	} else if n != 0 {
		return photos.GrantAnalysis{}, nil, errors.New("photo grant: unfinished analysis contains windows")
	}
	metadata := make([]byte, info.Size()-n)
	if _, err := file.ReadAt(metadata, n); err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	var lists analysisLists
	if err := json.Unmarshal(metadata, &lists); err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	analysis.Descriptions = lists.Descriptions
	return analysis, vectors, nil
}

// Remove takes assets out of a grant.
func (g Grants) Remove(ctx context.Context, grantID string, assetIDs []string) error {
	_, err := g.plain(ctx, GrantRequest{Op: GrantRemove, GrantID: grantID, AssetIDs: assetIDs})
	return err
}

// List lists every grant that is not revoked, a page per request.
func (g Grants) List(ctx context.Context) ([]photos.Grant, error) {
	var grants []photos.Grant
	after := ""
	for {
		response, err := g.plain(ctx, GrantRequest{Op: GrantList, After: after})
		if err != nil {
			return nil, err
		}
		grants = append(grants, response.Grants...)
		if response.Next == "" {
			return grants, nil
		}
		if response.Next <= after {
			return nil, errors.New("photo grant: list answered a page that does not move on")
		}
		after = response.Next
	}
}

// Revoke revokes a grant; revoking again changes nothing.
func (g Grants) Revoke(ctx context.Context, grantID string) error {
	_, err := g.plain(ctx, GrantRequest{Op: GrantRevoke, GrantID: grantID})
	return err
}

// GrantMessageLimit bounds a request or answer on the grant socket.
const GrantMessageLimit = grantMessageLimit
