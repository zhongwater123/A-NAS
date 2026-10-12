package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// Files is the part of files.Service the media center reads through. Every
// call runs as actor; in production the File Broker also needs the actor's
// session token in the context.
type Files interface {
	Tree(ctx context.Context, actor accounts.User, spaceID, directoryID string) ([]files.TreeEntry, error)
	Lookup(ctx context.Context, actor accounts.User, entryID string) (files.Entry, string, error)
	OpenContent(ctx context.Context, actor accounts.User, entryID string) (files.Content, error)
}

// Spaces lists the spaces an actor can see.
type Spaces interface {
	ListSpaces(ctx context.Context, actor accounts.User) ([]accounts.Space, error)
}

type Options struct {
	Files  Files
	Spaces Spaces
	// Sessions lists the valid sessions background work may borrow.
	Sessions func(context.Context) []accounts.Session
	// Processor runs FFmpeg. Without one, titles come from names and NFO
	// files only and every video is offered for direct play.
	Processor Processor
	// CacheDir keeps frames, resized artwork and converted subtitles.
	CacheDir string
	// MaxStreams bounds concurrent converted streams; it defaults to 2.
	MaxStreams int
	Now        func() time.Time
	Random     io.Reader
	Logger     *slog.Logger
}

type Service struct {
	store     *Store
	files     Files
	spaces    Spaces
	processor Processor
	cacheDir  string
	now       func() time.Time
	random    io.Reader
	logger    *slog.Logger
	streams   chan struct{}
	// kick wakes the background worker after a scan found new videos.
	kick chan struct{}

	sessions func(context.Context) []accounts.Session

	mu        sync.Mutex
	scanning  map[string]bool
	scanLocks map[string]*sync.Mutex

	// closing ends background scans started by requests.
	closing    context.Context
	close      context.CancelFunc
	background sync.WaitGroup
}

func NewService(store *Store, options Options) (*Service, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.MaxStreams <= 0 {
		options.MaxStreams = 2
	}
	if options.CacheDir != "" {
		for _, directory := range []string{"frames", "artwork", "subtitles"} {
			if err := os.MkdirAll(filepath.Join(options.CacheDir, directory), 0o700); err != nil {
				return nil, err
			}
		}
	}
	closing, closeService := context.WithCancel(context.Background())
	return &Service{
		closing: closing, close: closeService,
		store: store, files: options.Files, spaces: options.Spaces, processor: options.Processor,
		cacheDir: options.CacheDir, now: options.Now, random: options.Random, logger: options.Logger,
		streams: make(chan struct{}, options.MaxStreams), kick: make(chan struct{}, 1),
		scanning: map[string]bool{}, scanLocks: map[string]*sync.Mutex{}, sessions: options.Sessions,
	}, nil
}

// Close stops the scans that requests started and waits for them.
func (s *Service) Close() {
	s.close()
	s.background.Wait()
}

// Processing reports whether FFmpeg is available.
func (s *Service) Processing() bool { return s.processor != nil }

func (s *Service) randomID(prefix string) string {
	buffer := make([]byte, 12)
	if _, err := io.ReadFull(s.random, buffer); err != nil {
		panic(err)
	}
	return prefix + ":" + hex.EncodeToString(buffer)
}

func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// sessionFor picks a session that can read the library's space: the
// owner's for a personal space, the creator's or any other for the shared
// space. Sessions come from the account service, which only knows the ones
// it has seen and drops expired or revoked ones (ADR 0017).
func (s *Service) sessionFor(ctx context.Context, library libraryRow) (accounts.Session, bool) {
	if s.sessions == nil {
		return accounts.Session{}, false
	}
	var fallback *accounts.Session
	for _, session := range s.sessions(ctx) {
		if session.Token == "" || session.User.Status != accounts.UserStatusActive {
			continue
		}
		if library.SpaceKind == accounts.SpaceKindPrivate {
			if session.User.ID == library.OwnerUserID {
				return session, true
			}
			continue
		}
		if session.User.ID == library.CreatedBy {
			return session, true
		}
		if fallback == nil {
			fallback = &session
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return accounts.Session{}, false
}

func asUser(ctx context.Context, session accounts.Session) context.Context {
	return accounts.WithSessionToken(ctx, session.Token)
}
