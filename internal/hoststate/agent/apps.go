package agent

import (
	"context"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/appid"
)

func (c *Client) AppIdentity(ctx context.Context, id string) (appid.Identity, error) {
	var document wire.AppIdentityDocument
	if err := c.doJSON(ctx, c.operationClient, http.MethodPost, wire.AppsPathPrefix+id+"/identity", struct{}{}, &document); err != nil {
		return appid.Identity{}, err
	}
	return appid.Identity{Username: document.Username, UID: document.UID, GID: document.GID}, nil
}

func (c *Client) PrepareApp(ctx context.Context, id string, folders []string, shared bool) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, wire.AppsPathPrefix+id+"/prepare",
		wire.AppPreparationDocument{Folders: folders, Shared: shared}, nil)
}

func (c *Client) ReleaseApp(ctx context.Context, id string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, wire.AppsPathPrefix+id+"/release", struct{}{}, nil)
}

var _ appid.Host = (*Client)(nil)
