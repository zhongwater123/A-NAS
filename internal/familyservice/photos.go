// Package familyservice runs 家庭中心 as its own service and identity
// (ADR 0029): it alone holds the family store on the data volume, confirms
// every session with the Host Agent, and asks the photo service, with the
// reader's session, which channel photos that reader can see.
package familyservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// Photos asks the photo API, reached through photoAPI, about channel photos.
// On a NAS photoAPI forwards to the photo service's socket; development
// passes the photo API it serves in process. Either way the request carries
// only the reader's session (serviceproxy.SessionHeader), so the photo
// service answers by its own Policy.
type Photos struct {
	API http.Handler
}

// Lookup implements family.Photos.
func (p Photos) Lookup(ctx context.Context, actor family.Actor, ids []string) (map[string]family.Photo, error) {
	body, err := json.Marshal(photosapi.LookupRequest{IDs: ids})
	if err != nil {
		return nil, err
	}
	ctx = accounts.WithSessionToken(ctx, actor.Session)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, photosapi.LookupPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response := newBuffer()
	p.API.ServeHTTP(response, request)
	if response.status != http.StatusOK {
		return nil, fmt.Errorf("photo lookup answered %d", response.status)
	}
	items, err := photosapi.DecodeLookup(&response.body)
	if err != nil {
		return nil, err
	}
	found := make(map[string]family.Photo, len(items))
	for _, item := range items {
		found[item.Asset.ID] = family.Photo{ID: item.Asset.ID, TakenAt: item.Asset.TakenAt, Video: item.Asset.Video != nil, Shared: item.Shared}
	}
	return found, nil
}

// buffer collects a response served in process.
type buffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBuffer() *buffer { return &buffer{header: http.Header{}, status: http.StatusOK} }

func (b *buffer) Header() http.Header         { return b.header }
func (b *buffer) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *buffer) WriteHeader(status int)      { b.status = status }
