package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
)

// Relay holds a recording's audio where the recognition service can download
// it: Doubao's current recognition only takes a URL, which a NAS on a private
// network cannot give (ADR 0027).
type Relay interface {
	// Put stores the audio under key and returns a URL that reads it.
	Put(ctx context.Context, key string, audio []byte) (string, error)
	// Delete removes what Put stored; a missing object is not an error.
	Delete(ctx context.Context, key string) error
}

// StorageCredentials say which Volcengine TOS bucket relays the audio and
// with which access key.
type StorageCredentials struct {
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// Endpoint replaces the region's endpoint, for tests.
	Endpoint string
}

func (c StorageCredentials) usable() bool {
	return c.Region != "" && c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// relayTTL is how long a relayed recording can be downloaded: Doubao says a
// task usually finishes within 3 hours and may take longer at peak times.
// The object itself is deleted as soon as the task ends.
const relayTTL = 24 * time.Hour

// relayPrefix keeps A-NAS's objects together, for a lifecycle rule that
// clears whatever a crash left behind.
const relayPrefix = "a-nas-voice/"

// ErrRelayRejected: TOS refused the access key or the bucket.
var ErrRelayRejected = errors.New("the object storage refused the access key or the bucket")

// StorageRegions are the TOS regions an administrator can choose, as the
// SDK knows their endpoints.
func StorageRegions() []string {
	var regions []string
	for region := range tos.SupportedRegion() {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	return regions
}

// bucketName is TOS's rule for bucket names.
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// TOS relays audio through a Volcengine TOS bucket with presigned URLs.
type TOS struct {
	Credentials StorageCredentials
}

func (t TOS) client() (*tos.ClientV2, error) {
	endpoint := t.Credentials.Endpoint
	if endpoint == "" {
		endpoint = tos.SupportedRegion()[t.Credentials.Region]
	}
	if endpoint == "" {
		return nil, fmt.Errorf("%w: unknown region %q", ErrRelayRejected, t.Credentials.Region)
	}
	return tos.NewClientV2(endpoint,
		tos.WithRegion(t.Credentials.Region),
		tos.WithCredentials(tos.NewStaticCredentials(t.Credentials.AccessKeyID, t.Credentials.SecretAccessKey)),
		tos.WithMaxRetryCount(2),
		tos.WithRequestTimeout(10*time.Minute),
	)
}

func (t TOS) Put(ctx context.Context, key string, audio []byte) (string, error) {
	client, err := t.client()
	if err != nil {
		return "", err
	}
	defer client.Close()
	_, err = client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: t.Credentials.Bucket, Key: key, ContentLength: int64(len(audio)), ContentType: "audio/ogg",
		},
		Content: bytes.NewReader(audio),
	})
	if err != nil {
		return "", relayError(ctx, err)
	}
	signed, err := client.PreSignedURL(&tos.PreSignedURLInput{
		HTTPMethod: enum.HttpMethodGet, Bucket: t.Credentials.Bucket, Key: key, Expires: int64(relayTTL / time.Second),
	})
	if err != nil {
		_ = t.Delete(context.WithoutCancel(ctx), key)
		return "", fmt.Errorf("sign the relayed audio's URL: %w", err)
	}
	return signed.SignedUrl, nil
}

func (t TOS) Delete(ctx context.Context, key string) error {
	client, err := t.client()
	if err != nil {
		return err
	}
	defer client.Close()
	_, err = client.DeleteObjectV2(ctx, &tos.DeleteObjectV2Input{Bucket: t.Credentials.Bucket, Key: key})
	if err != nil && tos.StatusCode(err) != http.StatusNotFound {
		return relayError(ctx, err)
	}
	return nil
}

// relayError tells refused credentials and buckets, which an administrator
// must fix, from a service that could not be reached for now.
func relayError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	status := tos.StatusCode(err)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound:
		return fmt.Errorf("%w: %w: TOS %s", ErrRecognizerRejected, ErrRelayRejected, relayCode(err))
	case status == 0 || status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: object storage: %v", ErrRecognizerBusy, err)
	}
	return fmt.Errorf("object storage: %w", err)
}

func relayCode(err error) string {
	if code := tos.Code(err); code != "" {
		return code
	}
	return err.Error()
}
