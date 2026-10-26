// Package vectorindex is a service's client for its own vector index: a
// Qdrant instance that only this service reaches, through a Unix socket, and
// that holds nothing the service could not rebuild from its own Catalog
// (docs/architecture/vector-index.md, ADR 0028). It offers only the typed
// operations the owners need. Callers never pass a filter of their own, so a
// condition the index does not understand cannot widen a search or a delete.
//
// systemd starts the instance on the first connection and stops it once the
// connection has been idle for a while, so the client connects lazily, sends
// no keepalive pings and lets the connection go when idle.
package vectorindex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
)

// ServerVersion is the Qdrant release the client is built for, as
// major.minor. deploy/qdrant/qdrant.json pins the server, and a test keeps
// both and the client module on the same release.
const ServerVersion = "1.20"

// keyField holds a point's key in its payload.
const keyField = "key"

// scanPage is how many points a scan reads per request.
const scanPage = 1000

// idleTimeout closes an unused connection, so that the proxy and with it the
// instance can stop.
var idleTimeout = time.Minute

// ErrIncompatible means the instance runs another Qdrant release than the
// one the client is built for.
var ErrIncompatible = errors.New("the vector index runs another Qdrant release")

// ErrInvalid means a request the index would not answer as the caller meant,
// such as a keyword field named like the key.
var ErrInvalid = errors.New("invalid vector index request")

// Client talks to one vector index instance. Its zero value is not usable;
// call New. It is safe for concurrent use.
type Client struct {
	socket string
	logger *slog.Logger

	mu     sync.Mutex
	client *qdrant.Client
}

// New returns a client for the instance behind socket. It does not connect
// until the first request.
func New(socket string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{socket: socket, logger: logger}
}

// Close drops the connection, if any.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	err := c.client.Close()
	c.client = nil
	return err
}

// connection returns the open connection, opening it and checking the
// instance's release first when there is none.
func (c *Client) connection(ctx context.Context) (*qdrant.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	socket := c.socket
	client, err := qdrant.NewClient(&qdrant.Config{
		// The dialer below ignores the address; an IP literal needs no
		// name lookup.
		Host:                   "127.0.0.1",
		PoolSize:               1,
		KeepAliveTime:          -1,
		SkipCompatibilityCheck: true,
		Logger:                 c.logger,
		GrpcOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socket)
			}),
			grpc.WithIdleTimeout(idleTimeout),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open the vector index: %w", err)
	}
	reply, err := client.HealthCheck(ctx)
	if err == nil && !compatible(reply.GetVersion()) {
		err = fmt.Errorf("%w: %s, want %s", ErrIncompatible, reply.GetVersion(), ServerVersion)
	}
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	c.client = client
	return client, nil
}

func compatible(version string) bool {
	return version == ServerVersion || strings.HasPrefix(version, ServerVersion+".")
}

// PointID is the Qdrant point ID of a key: a UUID made of the first 16 bytes
// of the key's SHA-256, so that the same key always names the same point.
func PointID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// Collections lists the instance's collections.
func (c *Client) Collections(ctx context.Context) ([]string, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListCollections(ctx)
}

// CreateCollection creates a collection of normalised vectors of the given
// size, compared by dot product, with a keyword index on each of fields. The
// indexes exist before the first point, so that filtered search builds its
// links for them.
func (c *Client) CreateCollection(ctx context.Context, name string, dimensions int, fields ...string) error {
	if dimensions <= 0 || slices.Contains(fields, keyField) {
		return ErrInvalid
	}
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	if err := client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: name,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     uint64(dimensions),
			Distance: qdrant.Distance_Dot,
		}),
	}); err != nil {
		return fmt.Errorf("create vector collection %s: %w", name, err)
	}
	wait := true
	for _, field := range fields {
		if _, err := client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: name,
			FieldName:      field,
			FieldType:      qdrant.FieldType_FieldTypeKeyword.Enum(),
			Wait:           &wait,
		}); err != nil {
			return fmt.Errorf("index %s of vector collection %s: %w", field, name, err)
		}
	}
	return nil
}

// DeleteCollection deletes a collection and its points.
func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return client.DeleteCollection(ctx, name)
}

// Point is what the index keeps for one key.
type Point struct {
	// Key is the owner's stable ID of what the vector belongs to, such as a
	// content object.
	Key    string
	Vector []float32
	// Keywords are keyword fields to search within, such as the libraries
	// a photo is in.
	Keywords map[string][]string
}

// Upsert stores points, replacing those with the same keys, and returns once
// the instance has written them to its log.
func (c *Client) Upsert(ctx context.Context, collection string, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	structs := make([]*qdrant.PointStruct, 0, len(points))
	for _, point := range points {
		payload := map[string]any{keyField: point.Key}
		for field, values := range point.Keywords {
			if field == keyField {
				return ErrInvalid
			}
			list := make([]any, len(values))
			for i, value := range values {
				list[i] = value
			}
			payload[field] = list
		}
		structs = append(structs, &qdrant.PointStruct{
			Id:      qdrant.NewIDUUID(PointID(point.Key)),
			Vectors: qdrant.NewVectorsDense(point.Vector),
			Payload: qdrant.NewValueMap(payload),
		})
	}
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	wait := true
	_, err = client.Upsert(ctx, &qdrant.UpsertPoints{CollectionName: collection, Wait: &wait, Points: structs})
	return err
}

// Delete removes the points of keys; keys without a point are ignored.
func (c *Client) Delete(ctx context.Context, collection string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	ids := make([]*qdrant.PointId, len(keys))
	for i, key := range keys {
		ids[i] = qdrant.NewIDUUID(PointID(key))
	}
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	wait := true
	_, err = client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection,
		Wait:           &wait,
		Points:         qdrant.NewPointsSelectorIDs(ids),
	})
	return err
}

// Scope limits a search to the points whose keyword Field holds any of
// Values.
type Scope struct {
	Field  string
	Values []string
}

// Hit is a point a search found, with its similarity to the query.
type Hit struct {
	Key   string
	Score float32
}

// Search returns the limit points within scope most similar to vector, most
// similar first. A scope without values finds nothing: every search is
// scoped by its owner.
func (c *Client) Search(ctx context.Context, collection string, vector []float32, scope Scope, limit int) ([]Hit, error) {
	if scope.Field == "" || scope.Field == keyField || limit <= 0 {
		return nil, ErrInvalid
	}
	if len(scope.Values) == 0 {
		return nil, nil
	}
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	size := uint64(limit)
	points, err := client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: collection,
		Query:          qdrant.NewQueryDense(vector),
		Filter:         &qdrant.Filter{Must: []*qdrant.Condition{qdrant.NewMatchKeywords(scope.Field, scope.Values...)}},
		Limit:          &size,
		WithPayload:    qdrant.NewWithPayloadInclude(keyField),
	})
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(points))
	for _, point := range points {
		hits = append(hits, Hit{Key: point.GetPayload()[keyField].GetStringValue(), Score: point.GetScore()})
	}
	return hits, nil
}

// Entry is a point as a scan reads it: its key and the keyword fields asked
// for, without the vector.
type Entry struct {
	Key      string
	Keywords map[string][]string
}

// Scan calls visit for every point of the collection, in no set order, with
// the keyword fields named. It stops at the first error visit returns.
func (c *Client) Scan(ctx context.Context, collection string, fields []string, visit func(Entry) error) error {
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	page := uint32(scanPage)
	pages := client.ScrollAll(ctx, &qdrant.ScrollPoints{
		CollectionName: collection,
		Limit:          &page,
		WithPayload:    qdrant.NewWithPayloadInclude(append([]string{keyField}, fields...)...),
	})
	for {
		points, err := pages.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, point := range points {
			payload := point.GetPayload()
			entry := Entry{Key: payload[keyField].GetStringValue(), Keywords: make(map[string][]string, len(fields))}
			for _, field := range fields {
				var values []string
				for _, value := range payload[field].GetListValue().GetValues() {
					values = append(values, value.GetStringValue())
				}
				entry.Keywords[field] = values
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
	}
}
