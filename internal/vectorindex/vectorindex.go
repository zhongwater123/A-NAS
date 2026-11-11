// Package vectorindex is a service's client for its own vector index: a
// Qdrant instance that only this service reaches, through a Unix socket, and
// that holds nothing the service could not rebuild from its own Catalog
// (docs/architecture/vector-index.md, ADR 0028). It offers only the typed
// operations the owners need. Callers never pass a filter of their own, so a
// condition the index does not understand cannot widen a search or a delete.
//
// systemd starts the instance on the first connection and stops it soon after
// the last one closes, so the client connects lazily, sends no keepalive
// pings and lets the connection go when idle.
package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

// ServerVersion is the Qdrant release the client is built for, as
// major.minor. deploy/qdrant/qdrant.json pins the server, and a test keeps
// both and the client module on the same release.
const ServerVersion = "1.20"

// keyField holds a point's key in its payload.
const keyField = "key"

// scanPage is how many points a scan reads per request.
const scanPage = 1000

// idleTimeout closes an unused connection. The proxy exits seconds after the
// last connection closes, and the instance stops with it
// (anas-vectors-proxy@.service): an instance runs while its owner keeps a
// connection, so a client that is not Warm meets an instance that has to
// start.
var idleTimeout = time.Minute

// callTimeout bounds every call, the start of a cold instance included (its
// unit allows 90 s), so that a hung instance cannot hold its caller, or keep
// the connection busy and the instance running.
var callTimeout = 2 * time.Minute

// maxReply bounds a reply: a search with a floor returns every point above
// it, which in a large library is more than gRPC's default 4 MiB.
const maxReply = 256 << 20

// bounded is ctx with the call timeout.
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, callTimeout)
}

// pointNamespace is the namespace of point IDs (RFC 9562 name-based UUIDs).
var pointNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/zhongwater123/A-NAS/vectorindex"))

// ErrIncompatible means the instance runs another Qdrant release than the
// one the client is built for.
var ErrIncompatible = errors.New("the vector index runs another Qdrant release")

// ErrInvalid means a request the index would not answer as the caller meant,
// such as a keyword field named like the key.
var ErrInvalid = errors.New("invalid vector index request")

// ErrRejected means the index refused what was written, such as a vector of
// another size than its collection's; the same write would be refused again.
var ErrRejected = errors.New("the vector index rejected the write")

// ErrNoCollection means a search named a collection the instance does not
// hold, as after it lost its storage; the owner makes it again from its
// Catalog.
var ErrNoCollection = errors.New("the vector index has no such collection")

// Client talks to one vector index instance. Its zero value is not usable;
// call New. It is safe for concurrent use.
type Client struct {
	socket string
	logger *slog.Logger

	// connecting serialises opening the connection; client holds it once
	// open, so that Warm never waits for an instance that is starting.
	connecting sync.Mutex
	client     atomic.Pointer[qdrant.Client]
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
	c.connecting.Lock()
	defer c.connecting.Unlock()
	client := c.client.Swap(nil)
	if client == nil {
		return nil
	}
	return client.Close()
}

// Warm reports, without waiting, whether a connection to the instance is
// open and ready: the instance is running, and the next request does not
// wait for it to start.
func (c *Client) Warm() bool {
	client := c.client.Load()
	return client != nil && client.GetConnection().GetState() == connectivity.Ready
}

// connection returns the open connection, opening it and checking the
// instance's release first when there is none.
func (c *Client) connection(ctx context.Context) (*qdrant.Client, error) {
	if client := c.client.Load(); client != nil {
		return client, nil
	}
	c.connecting.Lock()
	defer c.connecting.Unlock()
	if client := c.client.Load(); client != nil {
		return client, nil
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
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxReply)),
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
	c.client.Store(client)
	return client, nil
}

// Prepare opens the connection, starting the instance when it is not
// running, and keeps an open one from going idle: the owner calls it while
// someone is about to search, so that the search does not wait for the
// instance to start.
func (c *Client) Prepare(ctx context.Context) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if client := c.client.Load(); client != nil {
		_, err := client.HealthCheck(ctx)
		return err
	}
	_, err := c.connection(ctx)
	return err
}

func compatible(version string) bool {
	return version == ServerVersion || strings.HasPrefix(version, ServerVersion+".")
}

// PointID is the Qdrant point ID of a key: a name-based UUID, so that the
// same key always names the same point.
func PointID(key string) string {
	return uuid.NewSHA1(pointNamespace, []byte(key)).String()
}

// Collections lists the instance's collections.
func (c *Client) Collections(ctx context.Context) ([]string, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListCollections(ctx)
}

// Collection describes a collection to create.
type Collection struct {
	Name string
	// Dimensions is the size of its vectors, which are normalised and
	// compared by dot product.
	Dimensions int
	// Keywords are the keyword fields searches are scoped by. Their indexes
	// exist before the first point, so that a graph links points by them.
	Keywords []string
	// Graph builds an HNSW graph for approximate search. Without one every
	// search scans exactly and nothing is built; up to about 100,000 vectors
	// that is as fast (docs/research/qdrant-retrieval-features.md).
	Graph bool
}

// CreateCollection creates a collection.
func (c *Client) CreateCollection(ctx context.Context, collection Collection) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if collection.Name == "" || collection.Dimensions <= 0 || slices.Contains(collection.Keywords, keyField) {
		return ErrInvalid
	}
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	params := &qdrant.VectorParams{Size: uint64(collection.Dimensions), Distance: qdrant.Distance_Dot}
	if !collection.Graph {
		none := uint64(0)
		params.HnswConfig = &qdrant.HnswConfigDiff{M: &none, PayloadM: &none}
	}
	if err := client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: collection.Name,
		VectorsConfig:  qdrant.NewVectorsConfig(params),
	}); err != nil {
		return fmt.Errorf("create vector collection %s: %w", collection.Name, err)
	}
	name := collection.Name
	wait := true
	for _, field := range collection.Keywords {
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
	ctx, cancel := bounded(ctx)
	defer cancel()
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
	ctx, cancel := bounded(ctx)
	defer cancel()
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
	if status.Code(err) == codes.InvalidArgument {
		return fmt.Errorf("%w: %v", ErrRejected, err)
	}
	return err
}

// Delete removes the points of keys; keys without a point are ignored.
func (c *Client) Delete(ctx context.Context, collection string, keys []string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
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

// Query asks for the Limit points within Scope most similar to Vector.
type Query struct {
	Vector []float32
	Scope  Scope
	Limit  int
	// Exact scans every point in scope even where a graph would answer
	// approximately.
	Exact bool
	// Floor, when set, leaves out the points less similar than it.
	Floor *float32
}

// Hit is a point a search found, with its similarity to the query.
type Hit struct {
	Key   string
	Score float32
}

// Search returns the points query asks for, most similar first. A scope
// without values finds nothing: every search is scoped by its owner.
func (c *Client) Search(ctx context.Context, collection string, query Query) ([]Hit, error) {
	found, err := c.SearchBatch(ctx, collection, []Query{query})
	if err != nil {
		return nil, err
	}
	return found[0], nil
}

// SearchBatch answers several queries in one request, each as Search would.
func (c *Client) SearchBatch(ctx context.Context, collection string, queries []Query) ([][]Hit, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	found := make([][]Hit, len(queries))
	var requests []*qdrant.QueryPoints
	var asked []int
	for i, query := range queries {
		if query.Scope.Field == "" || query.Scope.Field == keyField || query.Limit <= 0 {
			return nil, ErrInvalid
		}
		if len(query.Scope.Values) == 0 {
			continue
		}
		size := uint64(query.Limit)
		exact := query.Exact
		requests = append(requests, &qdrant.QueryPoints{
			CollectionName: collection,
			Query:          qdrant.NewQueryDense(query.Vector),
			Filter:         &qdrant.Filter{Must: []*qdrant.Condition{qdrant.NewMatchKeywords(query.Scope.Field, query.Scope.Values...)}},
			Limit:          &size,
			Params:         &qdrant.SearchParams{Exact: &exact},
			ScoreThreshold: query.Floor,
			WithPayload:    qdrant.NewWithPayloadInclude(keyField),
		})
		asked = append(asked, i)
	}
	if len(requests) == 0 {
		return found, nil
	}
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	results, err := client.QueryBatch(ctx, &qdrant.QueryBatchPoints{CollectionName: collection, QueryPoints: requests})
	if status.Code(err) == codes.NotFound {
		return nil, fmt.Errorf("%w: %v", ErrNoCollection, err)
	}
	if err != nil {
		return nil, err
	}
	if len(results) != len(requests) {
		return nil, fmt.Errorf("the vector index answered %d of %d queries", len(results), len(requests))
	}
	for n, result := range results {
		hits := make([]Hit, 0, len(result.GetResult()))
		for _, point := range result.GetResult() {
			hits = append(hits, Hit{Key: point.GetPayload()[keyField].GetStringValue(), Score: point.GetScore()})
		}
		found[asked[n]] = hits
	}
	return found, nil
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
	ctx, cancel := bounded(ctx)
	defer cancel()
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
