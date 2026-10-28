package appstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zhongwater123/A-NAS/internal/appid"
)

// ErrVolumeUnavailable refuses an install while the data volume, where the
// app's folders would be, is not available.
var ErrVolumeUnavailable = errors.New("the data volume is not available")

// Center installs and uninstalls the App Center's apps. Each app runs as its
// own Linux identity, which the host allocates; an install runs only the
// plan the owner confirmed for that identity, and only after the host
// prepared the app's folders (ADR 0008, ADR 0022). Uninstalling releases
// the app's access and keeps its identity and data for a reinstall.
type Center struct {
	store       Store
	host        appid.Host
	volumeReady func(context.Context) error
	logger      *slog.Logger
}

// NewCenter manages the apps of store. volumeReady fails while the data
// volume is unavailable; nil skips the check on development volumes.
func NewCenter(store Store, host appid.Host, volumeReady func(context.Context) error, logger *slog.Logger) *Center {
	if logger == nil {
		logger = slog.Default()
	}
	return &Center{store: store, host: host, volumeReady: volumeReady, logger: logger}
}

func (c *Center) Apps(ctx context.Context) ([]AppStatus, error) {
	return c.store.Apps(ctx)
}

func (c *Center) Icon(ctx context.Context, id string) ([]byte, string, error) {
	return c.store.Icon(ctx, id)
}

// Plan is what installing the app would run, as its own identity.
func (c *Center) Plan(ctx context.Context, id string) (Plan, error) {
	identity, err := c.identity(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	return c.store.Plan(ctx, id, identity)
}

// Install checks the confirmed plan against the app's identity, has the
// host prepare its folders, and only then starts Compose.
func (c *Center) Install(ctx context.Context, id, digest string) (Job, error) {
	if c.volumeReady != nil {
		if err := c.volumeReady(ctx); err != nil {
			return Job{}, fmt.Errorf("%w: %v", ErrVolumeUnavailable, err)
		}
	}
	identity, err := c.identity(ctx, id)
	if err != nil {
		return Job{}, err
	}
	plan, err := c.store.Plan(ctx, id, identity)
	if err != nil {
		return Job{}, err
	}
	if plan.Digest != digest {
		return Job{}, ErrPlanChanged
	}
	if err := c.host.PrepareApp(ctx, id, plan.HostFolders(), plan.SharesFolders()); err != nil {
		return Job{}, fmt.Errorf("%w: prepare app folders: %v", ErrUnavailable, err)
	}
	return c.store.Install(ctx, id, digest, identity)
}

// Uninstall starts removing the app's containers and releases its access to
// its folders.
func (c *Center) Uninstall(ctx context.Context, id string) (Job, error) {
	job, err := c.store.Uninstall(ctx, id)
	if err == nil && c.host != nil {
		if releaseErr := c.host.ReleaseApp(ctx, id); releaseErr != nil {
			c.logger.ErrorContext(ctx, "releasing app access failed", "app", id, "error", releaseErr)
		}
	}
	return job, err
}

// identity asks the host for the app's Linux identity.
func (c *Center) identity(ctx context.Context, id string) (Identity, error) {
	if c.host == nil {
		return Identity{}, fmt.Errorf("%w: no host for app identities", ErrUnavailable)
	}
	identity, err := c.host.AppIdentity(ctx, id)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return identity, nil
}
