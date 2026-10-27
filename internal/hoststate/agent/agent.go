// Package agent is the Product Service's client of the Host Agent: host
// state, metrics, volumes, credentials, snapshots, app identities, service
// logs and the task manager over HTTP and JSON on a Unix socket (ADR 0004).
// The root side is internal/hoststate/agent/rootagent, and neither links the
// other (#221).
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/taskmanager/taskicon"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const requestTimeout = 3 * time.Second

var ErrUnavailable = errors.New("host agent state is unavailable")

// SocketPath is wire.SocketPath.
func SocketPath(configured, runtimeDirectory string) (string, error) {
	return wire.SocketPath(configured, runtimeDirectory)
}

type Client struct {
	httpClient      *http.Client
	operationClient *http.Client
	// listingClient reads snapshot object listings, which take as long as
	// walking a space; its limit only stops a Host Agent that hangs.
	listingClient *http.Client
	// icons decodes the task icons the Host Agent names.
	icons taskicon.Loader
}

func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: requestTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{
		httpClient:      &http.Client{Transport: transport, Timeout: requestTimeout},
		operationClient: &http.Client{Transport: transport, Timeout: 15 * time.Minute},
		listingClient:   &http.Client{Transport: transport, Timeout: time.Hour},
	}
}

func (c *Client) Read(ctx context.Context) (hoststate.State, error) {
	var document wire.StateDocument
	if err := c.get(ctx, "/v1/state", &document); err != nil {
		return hoststate.State{}, err
	}
	state, err := document.ToDomain()
	if err != nil {
		return hoststate.State{}, fmt.Errorf("validate host agent response: %w", err)
	}
	return state, nil
}

// ReadMetrics returns the Host Agent's latest utilisation observation.
func (c *Client) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	var document wire.MetricsDocument
	if err := c.get(ctx, "/v1/metrics", &document); err != nil {
		return hoststate.Metrics{}, err
	}
	metrics, err := document.ToDomain()
	if err != nil {
		return hoststate.Metrics{}, fmt.Errorf("validate host agent response: %w", err)
	}
	return metrics, nil
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	return c.getWithClient(ctx, path, into, c.httpClient, 1<<20)
}

func (c *Client) getWithClient(ctx context.Context, path string, into any, client *http.Client, limit int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host-agent"+path, nil)
	if err != nil {
		return fmt.Errorf("create host agent request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("decode host agent response: %w", err)
	}
	return wire.EnsureEndOfJSON(decoder)
}

// maxSnapshotObjectLine bounds one line of a listing, far above the longest
// path Linux allows even with every byte escaped.
const maxSnapshotObjectLine = 1 << 20

func (c *Client) StartCreateVolume(ctx context.Context, request datavolume.CreateVolumeRequest) (datavolume.VolumeOperation, error) {
	var response datavolume.VolumeOperation
	err := c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/storage/volumes", wire.CreateVolumeDocument{
		PlanID: request.PlanID, Disks: request.Disks, Layout: request.Layout,
	}, &response)
	return response, err
}

func (c *Client) StartAddDisks(ctx context.Context, request datavolume.AddDisksRequest) (datavolume.VolumeOperation, error) {
	var response datavolume.VolumeOperation
	err := c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/storage/volumes/disks", wire.AddDisksDocument{
		PlanID: request.PlanID, FilesystemUUID: request.FilesystemUUID, Disks: request.Disks, Layout: request.Layout,
	}, &response)
	return response, err
}

func (c *Client) VolumeOperation(ctx context.Context, planID string) (datavolume.VolumeOperation, error) {
	var response datavolume.VolumeOperation
	err := c.doJSON(ctx, c.operationClient, http.MethodGet, wire.VolumeOperationsPath+url.PathEscape(planID), nil, &response)
	return response, err
}

func (c *Client) ContinueConversion(ctx context.Context) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/storage/volumes/conversion", nil, nil)
}

func (c *Client) SetCredential(ctx context.Context, request accountid.CredentialRequest) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPut, "/v1/accounts/credential", wire.CredentialDocument{
		UserID: request.UserID, PrivateSpaceID: request.PrivateSpaceID, Username: request.Username,
		Password: request.Password, Role: request.Role, UID: request.UID, Enabled: request.Enabled,
	}, nil)
}

func (c *Client) GrantViewing(ctx context.Context, request accountid.ViewingRequest) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/viewing-grants", wire.ViewingDocument{
		ID: request.ID, SpaceID: request.SpaceID, AdminUsername: request.AdminUsername,
		AdminUID: request.AdminUID, ExpiresAt: request.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, nil)
}

func (c *Client) RevokeViewing(ctx context.Context, id string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodDelete, "/v1/viewing-grants/"+url.PathEscape(id), nil, nil)
}

func (c *Client) SyncIdentities(ctx context.Context, identities []accountid.Identity) error {
	document := wire.IdentitiesDocument{Identities: make([]wire.IdentityDocument, len(identities))}
	for i, identity := range identities {
		document.Identities[i] = wire.IdentityDocument{Username: identity.Username, UID: identity.UID, Role: identity.Role, Enabled: identity.Enabled}
	}
	return c.doJSON(ctx, c.operationClient, http.MethodPut, "/v1/accounts/identities", document, nil)
}

func (c *Client) DisableCredential(ctx context.Context, username string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodDelete, "/v1/accounts/credential/"+url.PathEscape(username), nil, nil)
}

func (c *Client) Create(ctx context.Context, spaceID, snapshotID string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/snapshots", wire.SnapshotDocument{SpaceID: spaceID, SnapshotID: snapshotID}, nil)
}

// Objects reads a snapshot's listing line by line, so its size is not
// limited, and fails unless the listing ends with the count of its objects.
func (c *Client) Objects(ctx context.Context, snapshotID string, visit func(filesystem.SnapshotObject) error) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://host-agent/v1/snapshots/"+url.PathEscape(snapshotID)+"/objects", nil)
	if err != nil {
		return err
	}
	response, err := c.listingClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure wire.ErrorDocument
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		if response.StatusCode == http.StatusNotFound && failure.Error.Code == wire.SnapshotNotFoundCode {
			return filesystem.ErrNotFound
		}
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	lines := bufio.NewScanner(response.Body)
	lines.Buffer(make([]byte, 0, 64<<10), maxSnapshotObjectLine)
	count := 0
	for lines.Scan() {
		var line wire.SnapshotObjectLine
		decoder := json.NewDecoder(bytes.NewReader(lines.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&line); err != nil {
			return fmt.Errorf("decode host agent snapshot objects: %w", err)
		}
		if err := wire.EnsureEndOfJSON(decoder); err != nil {
			return err
		}
		switch {
		case line.Object != nil && line.End == nil && line.Error == nil:
			count++
			if err := visit(*line.Object); err != nil {
				return err
			}
		case line.End != nil && line.Object == nil && line.Error == nil:
			if line.End.Count != count {
				return fmt.Errorf("host agent counted %d snapshot objects but sent %d", line.End.Count, count)
			}
			if lines.Scan() {
				return errors.New("host agent sent snapshot objects after the end")
			}
			return lines.Err()
		case line.Error != nil && line.Object == nil && line.End == nil:
			return fmt.Errorf("%w: snapshot objects: %s", ErrUnavailable, line.Error.Code)
		default:
			return errors.New("decode host agent snapshot objects: invalid line")
		}
	}
	if err := lines.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return fmt.Errorf("%w: snapshot objects ended early", ErrUnavailable)
}

func (c *Client) Open(ctx context.Context, snapshotID, key string) (io.ReadCloser, filesystem.SnapshotObject, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://host-agent/v1/snapshots/"+url.PathEscape(snapshotID)+"/object?key="+url.QueryEscape(key), nil)
	if err != nil {
		return nil, filesystem.SnapshotObject{}, err
	}
	response, err := c.operationClient.Do(request)
	if err != nil {
		return nil, filesystem.SnapshotObject{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, filesystem.SnapshotObject{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	size, err := strconv.ParseInt(response.Header.Get("X-A-NAS-Object-Size"), 10, 64)
	if err != nil {
		_ = response.Body.Close()
		return nil, filesystem.SnapshotObject{}, errors.New("invalid snapshot object size")
	}
	name, err := url.QueryUnescape(response.Header.Get("X-A-NAS-Object-Name"))
	if err != nil {
		_ = response.Body.Close()
		return nil, filesystem.SnapshotObject{}, errors.New("invalid snapshot object name")
	}
	object := filesystem.SnapshotObject{
		Key: response.Header.Get("X-A-NAS-Object-Key"), Name: name,
		Kind: filesystem.EntryKind(response.Header.Get("X-A-NAS-Object-Kind")), SizeBytes: size,
	}
	return response.Body, object, nil
}

func (c *Client) Delete(ctx context.Context, spaceID, snapshotID string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodDelete,
		"/v1/snapshots/"+url.PathEscape(snapshotID)+"?spaceId="+url.QueryEscape(spaceID), nil, nil)
}

func (c *Client) Prune(ctx context.Context, keep []string) (filesystem.SnapshotPrune, error) {
	if keep == nil {
		keep = []string{}
	}
	var response filesystem.SnapshotPrune
	err := c.doJSON(ctx, c.operationClient, http.MethodPut, "/v1/snapshots", wire.SnapshotKeepDocument{Keep: keep}, &response)
	if errors.Is(err, accountid.ErrVolumeUnavailable) {
		return filesystem.SnapshotPrune{}, filesystem.ErrVolumeUnavailable
	}
	return response, err
}

func (c *Client) doJSON(ctx context.Context, client *http.Client, method, path string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://host-agent"+path, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure wire.ErrorDocument
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		if response.StatusCode == http.StatusConflict && failure.Error.Code == wire.IdentityConflictCode {
			return accountid.ErrIdentityConflict
		}
		if response.StatusCode == http.StatusLocked && failure.Error.Code == wire.VolumeUnavailableCode {
			return accountid.ErrVolumeUnavailable
		}
		if response.StatusCode == http.StatusConflict && failure.Error.Code == wire.NoConversionCode {
			return datavolume.ErrNoConversion
		}
		if response.StatusCode == http.StatusNotFound && failure.Error.Code == wire.OperationNotFoundCode {
			return datavolume.ErrOperationNotFound
		}
		if response.StatusCode == http.StatusConflict && failure.Error.Code == wire.OperationConflictCode {
			return fmt.Errorf("%w: %s", datavolume.ErrPlanState, failure.Error.Message)
		}
		if response.StatusCode == http.StatusNotFound && failure.Error.Code == wire.SnapshotNotFoundCode {
			return filesystem.ErrNotFound
		}
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(responseBody); err != nil {
		return err
	}
	return wire.EnsureEndOfJSON(decoder)
}

var _ hoststate.Observer = (*Client)(nil)

var _ datavolume.VolumeExecutor = (*Client)(nil)

var _ accountid.CredentialProvisioner = (*Client)(nil)

var _ accountid.IdentitySynchronizer = (*Client)(nil)

var _ accountid.ViewingProvisioner = (*Client)(nil)

var _ filesystem.SnapshotBackend = (*Client)(nil)
