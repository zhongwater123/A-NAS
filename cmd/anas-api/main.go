package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/aiworker"
	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	appstoreagent "github.com/zhongwater123/A-NAS/internal/appstore/agent"
	fakeappstore "github.com/zhongwater123/A-NAS/internal/appstore/fake"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/assistantchanges"
	"github.com/zhongwater123/A-NAS/internal/containers"
	containeragent "github.com/zhongwater123/A-NAS/internal/containers/agent"
	fakecontainers "github.com/zhongwater123/A-NAS/internal/containers/fake"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
	"github.com/zhongwater123/A-NAS/internal/feedback"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/media"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/personalization"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
	"github.com/zhongwater123/A-NAS/internal/terminal"
	"github.com/zhongwater123/A-NAS/internal/transfers"
	"github.com/zhongwater123/A-NAS/internal/transfersapi"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
	"github.com/zhongwater123/A-NAS/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// The photo service runs from the same binary as its own process and
	// identity (ADR 0011), so releases keep two hashed binaries.
	// systemd starts a FFmpeg worker per connection, with the connection as
	// standard input (ADR 0017).
	if len(os.Args) == 2 && os.Args[1] == ffmpegWorkerCommand {
		if err := runFFmpegWorker(); err != nil {
			logger.Error("A-NAS FFmpeg worker failed", "error", err)
			os.Exit(1)
		}
		return
	}
	// Code without a logger of its own, such as decode guards, logs here too.
	slog.SetDefault(logger)
	if len(os.Args) == 2 && os.Args[1] == "share-service" {
		if err := runShareService(logger); err != nil {
			logger.Error("A-NAS share service stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == photoServiceCommand {
		if err := runPhotoService(logger); err != nil {
			logger.Error("A-NAS photo service stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("A-NAS API stopped", "error", err)
		os.Exit(1)
	}
}

const (
	photoServiceCommand = "photo-service"
	ffmpegWorkerCommand = "ffmpeg-worker"
)

func runFFmpegWorker() error {
	if os.Geteuid() == 0 {
		return errors.New("the FFmpeg worker must run under its own dynamic identity, never root")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ffmpegworker.ServeStandardInput(ctx, ffmpegworker.Tools{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe", HEIFThumbnailer: "/usr/bin/heif-thumbnailer"})
}

func runPhotoService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the photo service must run as its own identity, never root")
	}
	config, err := loadPhotoServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lookup := sessionlookup.NewClient(config.SessionSocket)
	var vectors photos.VectorIndex
	if config.VectorSocket != "" {
		client := vectorindex.New(config.VectorSocket, logger)
		defer func() { _ = client.Close() }()
		vectors = client
	}
	return photoservice.Run(ctx, photoservice.Config{
		Root:                config.Root,
		SocketPath:          config.Socket,
		Sessions:            lookup,
		AssistantSocketPath: config.AssistantSocket,
		Accounts:            lookup,
		Logger:              logger,
		AI:                  aiworker.Client{SocketPath: config.AISocket},
		AIQuietPeriod:       config.Quiet,
		Photos:              photos.Options{Media: config.Media.client()},
		Vectors:             vectors,
	})
}

func run(logger *slog.Logger) error {
	config, err := loadConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	live, stateDirectory, volumeRoot := config.Live(), config.StateDir, config.DataMount
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Background work stops as soon as the process is told to stop, while
	// open requests drain, so that both fit in the unit's TimeoutStopSec; or
	// when the server fails. run waits for it before returning, and so before
	// the deferred closes of the stores it writes to.
	background, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	var tasks errgroup.Group
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return err
	}
	reader, dataSource, operations := configuredServices(config)
	// Development shows invented service logs; the Host Agent reads the
	// journal (ADR 0020).
	var systemLogs systemlog.Reader = systemlog.NewFake(nil)
	if logs, ok := operations.(systemlog.Reader); ok && live {
		systemLogs = logs
	}
	accountStore, err := accounts.OpenSQLite(filepath.Join(stateDirectory, "control.db"))
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "control.db", accountStore)
	fileStore, err := files.OpenSQLite(filepath.Join(stateDirectory, "files.db"))
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "files.db", fileStore)
	storageStore, err := storage.OpenSQLite(filepath.Join(stateDirectory, "storage.db"))
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "storage.db", storageStore)
	accountService := accounts.NewService(accountStore, operations, accounts.Options{})
	storageService, err := storage.OpenService(reader, operations, storage.Options{Store: storageStore})
	if err != nil {
		return err
	}
	defer storageService.Close()
	terminalConfig := terminal.Config{Enabled: config.TerminalEnabled}
	var broker *filebroker.Client
	var fileService *files.Service
	var volumeGuard files.VolumeGuard
	if live {
		// The guard runs before every file request, so the expected UUID
		// comes from the stored volume, not from a Host Agent disk read; the
		// guard itself proves the mounted volume carries it.
		volumeGuard = sandboxedVolumeGuard{files.BtrfsVolumeGuard{ExpectedFilesystemUUID: func(context.Context) (string, error) {
			return storageService.DataVolumeFilesystemUUID(), nil
		}}}
		broker = filebroker.NewClient(config.FileBrokerSocket)
		fileService = files.NewService(fileStore, volumeRoot, accountService, broker, files.Options{
			VolumeGuard: volumeGuard, SnapshotBackend: operations,
		})
		// Terminal shells run as the signed-in administrator, not as a-nas.
		terminalConfig.Spawner = broker
	} else {
		if err := os.MkdirAll(volumeRoot, 0o700); err != nil {
			return err
		}
		fileService = files.NewLocalService(fileStore, volumeRoot, accountService, files.Options{AllowUnverifiedVolume: true})
	}
	// The media center and the music library share one FFmpeg boundary and
	// adapt its operations to their own needs (ADR 0017, ADR 0018).
	mediaWorker := configuredFFmpegWorker(config)
	musicStore, err := music.OpenSQLite(filepath.Join(stateDirectory, "music.db"))
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "music.db", musicStore)
	// Runs before the deferred musicStore.Close, after the server stopped.
	musicService := music.New(musicStore, filepath.Join(stateDirectory, "music-covers"), fileService, accountService, music.Options{Logger: logger, Worker: mediaWorker})
	defer musicService.Close()
	// The Product Service cannot reach the data volume in production; there
	// the photo service owns the photos subvolume under its own identity and
	// the Product Service only forwards the photo API to it (ADR 0011).
	// Development opens the library in this process.
	var photoService *photos.Service
	var photoAI photos.Embedder
	var photoAPI http.Handler = photosapi.NewProxy(config.PhotosSocket, logger)
	if !live {
		// Development reaches an AI Worker only when ANAS_AI_SOCKET names
		// one, so development without the AI component keeps local AI off.
		if config.DevelopmentAISocket != "" {
			photoAI = aiworker.Client{SocketPath: config.DevelopmentAISocket}
		}
		photoService, err = photos.Open(filepath.Join(volumeRoot, "photos"), photos.Options{AI: photoAI, Media: mediaWorker})
		if err != nil {
			return err
		}
		defer closeAtExit(logger, "photo library", photoService)
		photoAPI = photosapi.New(photoService, logger)
	}
	mediaStore, err := media.OpenSQLite(filepath.Join(stateDirectory, "media.db"))
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "media.db", mediaStore)
	var processor media.Processor
	if mediaWorker != nil {
		processor = media.WorkerProcessor{Worker: mediaWorker}
	}
	previewService, err := filepreview.NewWithCache(fileService, mediaWorker, filepath.Join(stateDirectory, "file-previews"))
	if err != nil {
		logger.Warn("file preview disk cache unavailable; using memory cache", "error", err)
		previewService = filepreview.New(fileService, mediaWorker)
	}
	mediaService, err := media.NewService(mediaStore, media.Options{
		Files: fileService, Spaces: accountService, Sessions: accountService.RememberedSessions,
		Processor: processor, CacheDir: filepath.Join(stateDirectory, "media-cache"), Logger: logger,
	})
	if err != nil {
		return err
	}
	defer mediaService.Close()
	feedbackStore, err := feedback.Open(filepath.Join(stateDirectory, "feedback.db"), feedback.Options{})
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "feedback.db", feedbackStore)
	personalizationStore, err := personalization.Open(filepath.Join(stateDirectory, "personalization.db"))
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "personalization.db", personalizationStore)
	transferService, err := transfers.Open(filepath.Join(stateDirectory, "transfers.db"), fileService, accountService, transfers.Options{
		Sessions: accountService.RememberedSessions, FilesChanged: mediaService.SpaceChanged, Logger: logger,
	})
	if err != nil {
		return err
	}
	defer closeAtExit(logger, "transfers.db", transferService)
	transferAPI, err := transfersapi.New(transferService, logger)
	if err != nil {
		return err
	}
	terminals := terminal.New(terminalConfig, logger)

	containerManager, appStore, containerSource, err := configuredContainers(config)
	if err != nil {
		return err
	}

	appOptions := appstoreapi.Options{Host: appstoreapi.DevelopmentHost{}, Logger: logger}
	if live {
		host, ok := operations.(appid.Host)
		if !ok {
			return errors.New("host agent client cannot prepare apps")
		}
		appOptions.Host = host
		appOptions.VolumeReady = func(ctx context.Context) error { return volumeGuard.Check(ctx, volumeRoot, false) }
	}

	shareAPI, closeShares, err := configuredShares(ctx, config, accountService, fileService, mediaWorker, logger)
	if err != nil {
		return err
	}
	defer closeShares()
	assistant := assistantAPI(background, config.Assistant, logger, broker, accountService)
	apiHandler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: reader, DataSource: dataSource, ProductVersion: version,
		Accounts: accountService, Files: fileService, Storage: storageService, Personalization: personalizationStore,
		Terminal:     terminals,
		Containers:   containersapi.New(containerManager, containerSource, logger),
		Apps:         appstoreapi.New(appStore, appstoreapi.DataSource(containerSource), appOptions),
		AppShortcuts: appstoreapi.NewShortcuts(appStore),
		Photos:       photoAPI,
		Shares:       shareAPI,
		Media:        mediaapi.New(mediaService, logger),
		Music:        musicService,
		FilePreviews: previewService,
		Feedback:     feedbackapi.New(feedbackStore, logger),
		Transfers:    transferAPI,
		Assistant:    assistant,
		SystemLogs:   systemLogs,
		// Web uploads and other changes rescan the media libraries of their
		// space a few seconds later.
		FilesChanged: mediaService.SpaceChanged,
		Logger:       logger,
	})
	handler, err := webui.NewWithOptions(apiHandler, webui.Options{
		ScreensaverDirectory: config.ScreensaverDirectory,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: config.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		// Panics in handlers and TLS or protocol errors are logged as JSON
		// like everything else.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	tasks.Go(func() error { syncIdentities(background, accountService.SyncIdentities, logger); return nil })
	tasks.Go(func() error { runDirectoryReconciliation(background, accountService, fileService, logger); return nil })
	// The Host Agent takes scheduled snapshots, so they need no session. A
	// snapshot that stopping interrupts is deleted again before the catalog
	// closes.
	tasks.Go(func() error { fileService.RunSnapshotSchedules(background, accountService, logger); return nil })
	tasks.Go(func() error { mediaService.Run(background); return nil })
	// Its last flush of transfer progress lands before transfers.db closes.
	tasks.Go(func() error { transferService.Run(background); return nil })
	if photoService != nil {
		tasks.Go(func() error {
			photoservice.RunBackground(background, photoService, logger, photoAI, photoservice.NewGate(photoservice.QuietPeriod, logger), nil)
			return nil
		})
	}
	// The assistant's kept instances record task notices in control.db.
	tasks.Go(func() error {
		<-background.Done()
		assistant.Wait()
		return nil
	})
	listener, err := net.Listen("tcp", config.HTTPAddr)
	if err == nil {
		logger.Info("A-NAS API listening", "address", config.HTTPAddr, "version", version, "data_source", dataSource, "terminal", config.TerminalEnabled, "containers", containerManager != nil)
		err = serve(ctx, server, listener, terminals, logger)
	}
	stopBackground()
	_ = tasks.Wait()
	return err
}

// shutdownGrace is how long stopping waits for open requests. The system
// unit's TimeoutStopSec of 20 s leaves room for the background work that is
// still stopping then.
var shutdownGrace = 15 * time.Second

// serve runs server on listener until the process is told to stop or the
// server fails. Stopping ends the terminal sessions, which hijacked their
// connections, while it waits shutdownGrace for the other requests; each has
// the whole grace period. Connections still open then, such as long media,
// music or download streams, are closed, which is not a failure.
func serve(ctx context.Context, server *http.Server, listener net.Listener, terminals *terminal.Handler, logger *slog.Logger) error {
	listenError := make(chan error, 1)
	go func() { listenError <- server.Serve(listener) }()
	select {
	case err := <-listenError:
		return err
	case <-ctx.Done():
	}
	logger.Info("A-NAS API shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	terminalsEnded := make(chan struct{})
	go func() {
		defer close(terminalsEnded)
		if err := terminals.Shutdown(shutdownCtx); err != nil {
			logger.Warn("terminal sessions did not end in time", "error", err)
		}
	}()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("requests still open when stopping were cut off", "error", err)
		_ = server.Close()
	}
	<-terminalsEnded
	if err := <-listenError; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// closeAtExit closes what run opened, once the server has stopped. The
// process is on its way out, so a failure can only be logged.
func closeAtExit(logger *slog.Logger, name string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		logger.Error("A-NAS API could not close a store", "store", name, "error", err)
	}
}

// sandboxedVolumeGuard proves only that the intended data volume is mounted.
// The Product Service's ProtectSystem=strict view always shows the volume
// read-only, and it never writes there: File Broker workers report a
// read-only volume as unavailable, and the Host Agent rechecks writability
// before it prepares app folders.
type sandboxedVolumeGuard struct{ files.VolumeGuard }

func (g sandboxedVolumeGuard) Check(ctx context.Context, volumeRoot string, _ bool) error {
	return g.VolumeGuard.Check(ctx, volumeRoot, false)
}

// configuredFFmpegWorker picks where FFmpeg runs. Production uses the
// sandboxed worker socket; development runs FFmpeg in this process when it
// is installed, and otherwise the media center and music work without it.
func configuredFFmpegWorker(config Config) ffmpegworker.Worker {
	switch config.MediaWorker {
	case mediaWorkerSocket:
		return config.Media.client()
	case mediaWorkerLocal:
		if tools, ok := ffmpegworker.LocalTools(); ok {
			return ffmpegworker.Local{Tools: tools}
		}
	}
	return nil
}

// The pause before synchronizing identities again after a failure, doubling
// up to identitySyncRetryMax. Tests shorten them.
var (
	identitySyncRetryMin = 2 * time.Second
	identitySyncRetryMax = 15 * time.Minute
)

// syncIdentities converges host accounts on the control plane at startup, so
// accounts created before ADR 0008 receive their A-NAS UID and groups, and
// disabled accounts lose their SMB sessions. It tries again until it
// succeeds: when both services restart together, the Host Agent may not be
// accepting connections yet.
func syncIdentities(ctx context.Context, sync func(context.Context) error, logger *slog.Logger) {
	delay := identitySyncRetryMin
	for attempt := 1; ; attempt++ {
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := sync(syncCtx)
		cancel()
		if err == nil {
			logger.InfoContext(ctx, "identity synchronization succeeded", "attempt", attempt)
			return
		}
		if ctx.Err() != nil {
			return
		}
		logger.ErrorContext(ctx, "identity synchronization failed", "error", err, "attempt", attempt, "retry", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, identitySyncRetryMax)
	}
}

func runDirectoryReconciliation(ctx context.Context, accountService *accounts.Service, fileService *files.Service, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// File operations run as the user through the File Broker, which
			// needs a valid session; users without one are reconciled on their
			// next visit.
			for _, session := range accountService.RememberedSessions(ctx) {
				userCtx := accounts.WithSessionToken(ctx, session.Token)
				if err := fileService.ReconcileVisibleSpaces(userCtx, session.User); err != nil && !errors.Is(err, files.ErrVolumeUnavailable) {
					logger.ErrorContext(ctx, "directory reconciliation failed", "user_id", session.User.ID, "error", err)
				}
			}
		}
	}
}

type productOperations interface {
	accounts.CredentialProvisioner
	storage.VolumeExecutor
	files.SnapshotBackend
}

// configuredServices reaches the host through the Host Agent on a NAS, and
// through simulated disks and storage in development.
func configuredServices(config Config) (hoststate.Observer, httpapi.DataSource, productOperations) {
	if config.Live() {
		client := agent.NewClient(config.HostAgentSocket)
		return client, httpapi.DataSourceLive, client
	}
	simulated := newSimulatedStorage(fake.NewWithSpareDisk())
	return simulated, httpapi.DataSourceSimulated, &developmentOperations{simulatedStorage: simulated}
}

// developmentOperations stand in for the Host Agent in fake mode.
type developmentOperations struct{ *simulatedStorage }

func (*developmentOperations) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (*developmentOperations) DisableCredential(context.Context, string) error { return nil }

// Development volumes have no ACLs; viewing access is decided by the Policy.
func (*developmentOperations) GrantViewing(context.Context, accounts.ViewingRequest) error {
	return nil
}
func (*developmentOperations) RevokeViewing(context.Context, string) error { return nil }
func (*developmentOperations) Create(context.Context, string, string) error {
	return errors.New("development snapshot backend is not configured")
}
func (*developmentOperations) Objects(context.Context, string, func(files.SnapshotObject) error) error {
	return files.ErrNotFound
}
func (*developmentOperations) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return nil, files.SnapshotObject{}, files.ErrNotFound
}
func (*developmentOperations) Delete(context.Context, string, string) error { return files.ErrNotFound }
func (*developmentOperations) Survey(context.Context, []string) (files.SnapshotSurvey, error) {
	return files.SnapshotSurvey{}, errors.New("development snapshot backend is not configured")
}

// configuredContainers selects container management and the App Center
// together, since both run through the container agent.
func configuredContainers(config Config) (containers.Manager, appstore.Store, containersapi.DataSource, error) {
	switch config.Containers {
	case containersFake:
		apps, err := fakeappstore.New(700 * time.Millisecond)
		if err != nil {
			return nil, nil, "", err
		}
		return fakecontainers.New(), apps, containersapi.DataSourceSimulated, nil
	case containersAgent:
		socket := config.ContainerAgentSocket
		return containeragent.NewClient(socket), appstoreagent.NewClient(socket), containersapi.DataSourceLive, nil
	default:
		return nil, nil, containersapi.DataSourceLive, nil
	}
}

// assistantAPI configures the AI assistant (ADR 0024). Without a socket
// directory it answers assistant_unavailable and the desktop keeps the
// assistant as planned. On a NAS every connection to a member's instance goes
// through the File Broker, the session gate (decision 10), which also
// executes the assistant's changes; development dials the sockets directly
// with their tokens and may name the spike's change broker. The account store
// keeps the notices of the members' scheduled tasks. Its background work ends
// with background.
func assistantAPI(background context.Context, config AssistantConfig, logger *slog.Logger, broker *filebroker.Client, notices assistantapi.TaskNotices) *assistantapi.Handler {
	options := assistantapi.Options{SocketDir: config.SocketDir, Notices: notices, Logger: logger, Context: background}
	switch {
	case config.SocketDir == "":
	case broker != nil:
		options.Connector = gatedAssistant{broker}
	default:
		options.Connector = assistantapi.NewSocketConnector(config.SocketDir, config.TokenDir)
	}
	switch {
	case broker != nil:
		options.Changes = executedChanges{broker}
	case config.SpikeBrokerSocket != "":
		options.Changes = assistantapi.BrokerChanges(config.SpikeBrokerSocket)
	}
	return assistantapi.New(options)
}

// executedChanges is the File Broker's executor of the assistant's changes. It
// acts for the member whose session the request carries, whatever account
// the caller names.
type executedChanges struct{ broker *filebroker.Client }

func (e executedChanges) Prepare(ctx context.Context, _ string, submission assistantapi.Submission) (assistantapi.Change, error) {
	return asChange(e.broker.PrepareChange(ctx, submission.SessionID, submission.RequestID, submission.Args))
}

func (e executedChanges) Decide(ctx context.Context, _, id string, decision assistantapi.Decision) (assistantapi.Change, error) {
	return asChange(e.broker.DecideChange(ctx, id, string(decision)))
}

func (e executedChanges) Get(ctx context.Context, _, id string) (assistantapi.Change, error) {
	return asChange(e.broker.GetChange(ctx, id))
}

func asChange(raw json.RawMessage, err error) (assistantapi.Change, error) {
	switch {
	case errors.Is(err, assistantchanges.ErrInvalid):
		return assistantapi.Change{}, fmt.Errorf("%w: %v", assistantapi.ErrInvalidSubmission, err)
	case errors.Is(err, assistantchanges.ErrDecided):
		return assistantapi.Change{}, assistantapi.ErrChangeDecided
	case errors.Is(err, assistantchanges.ErrNotFound):
		return assistantapi.Change{}, assistantapi.ErrChangeNotFound
	case errors.Is(err, files.ErrForbidden):
		return assistantapi.Change{}, assistantapi.ErrChangeSession
	case err != nil:
		return assistantapi.Change{}, err
	}
	var change assistantapi.Change
	if err := json.Unmarshal(raw, &change); err != nil || change.ID == "" {
		return assistantapi.Change{}, errors.New("the executor answered without a change")
	}
	change.Raw = raw
	return change, nil
}

// gatedAssistant reaches a member's assistant through the File Broker, which
// checks the session itself and cuts the connection when it ends.
type gatedAssistant struct{ broker *filebroker.Client }

func (g gatedAssistant) Connect(ctx context.Context, account string, fresh bool) (net.Conn, string, error) {
	conn, err := g.broker.ConnectAssistant(ctx, fresh)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", assistantapi.ErrNoAssistant
	case errors.Is(err, files.ErrForbidden):
		return nil, "", assistantapi.ErrSessionRejected
	case err != nil:
		return nil, "", err
	}
	if conn.Username != account {
		_ = conn.Close()
		return nil, "", assistantapi.ErrSessionRejected
	}
	return conn, conn.Token, nil
}
