package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The time SearXNG may take: its engines time out after 5 seconds and a
// search after 10 (deploy/search/settings.yml); the image proxy follows
// redirects to remote hosts.
const (
	configTimeout    = 5 * time.Second
	searchTimeout    = 12 * time.Second
	thumbnailTimeout = 15 * time.Second
	// MaxThumbnailBytes is the most SearXNG's image proxy passes on.
	MaxThumbnailBytes = 5 << 20
	maxResponseBytes  = 8 << 20
)

// SearXNG reaches the web search service over its Unix socket.
type SearXNG struct {
	client *http.Client
}

func NewSearXNG(socketPath string) *SearXNG {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns: 8, IdleConnTimeout: time.Minute,
	}
	return &SearXNG{client: &http.Client{
		Transport: transport,
		// SearXNG follows the remote site's redirects itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *SearXNG) Engines(ctx context.Context) ([]EngineInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, configTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://searxng/config", nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("SearXNG's /config answered %d", response.StatusCode)
	}
	var config struct {
		Engines []EngineInfo `json:"engines"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&config); err != nil {
		return nil, fmt.Errorf("decode SearXNG's /config: %w", err)
	}
	if len(config.Engines) == 0 {
		return nil, fmt.Errorf("SearXNG loaded no engines")
	}
	return config.Engines, nil
}

func (s *SearXNG) Search(ctx context.Context, form url.Values) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://searxng/search", strings.NewReader(form.Encode()))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Response{}, fmt.Errorf("SearXNG answered %d", response.StatusCode)
	}
	var decoded Response
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("decode SearXNG's answer: %w", err)
	}
	return decoded, nil
}

func (s *SearXNG) Thumbnail(ctx context.Context, imageURL, signature string) (Image, error) {
	ctx, cancel := context.WithTimeout(ctx, thumbnailTimeout)
	defer cancel()
	address := "http://searxng/image?" + url.Values{"url": {imageURL}, "h": {signature}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return Image{}, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return Image{}, err
	}
	defer response.Body.Close()
	// SearXNG answered, so it runs: any other status is the remote host's,
	// which it passes on (403 from hotlink protection, 5xx), or 400 for an
	// address it could not fetch. The window shows its placeholder either way.
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Image{}, ErrNotFound
	}
	// The proxy also passes binary/octet-stream on; the window shows images only.
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "image/") || mediaType == "image/svg+xml" {
		return Image{}, ErrNotFound
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxThumbnailBytes+1))
	if err != nil {
		return Image{}, err
	}
	if len(body) > MaxThumbnailBytes {
		return Image{}, ErrNotFound
	}
	return Image{ContentType: mediaType, Body: body}, nil
}

var _ Backend = (*SearXNG)(nil)
