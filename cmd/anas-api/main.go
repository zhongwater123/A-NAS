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
	"strings"
	"syscall"
	"time"

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
	"github.com/zhongwater123/A-NAS/internal/feedback"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/media"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
	"github.com/zhongwater123/A-NAS/internal/terminal"
	"github.com/zhongwater123/A-NAS/internal/transfers"
	"github.com/zhongwater123/A-NAS/internal/transfersapi"
	"github.com/zhongwater123/A-NAS/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// The photo service runs from the same binary as its own process and
	// identity (ADR 0011), so releases keep two hashed binaries.
	// systemd starts a media worker per connection, with the connection as
	// standard input (ADR 0017).
	if len(os.Args) == 2 && os.Args[1] == mediaWorkerCommand {
		if err := runMediaWorker(); err != nil {
			logger.Error("A-NAS media worker failed", "error", err)
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
	mediaWorkerCommand  = "media-worker"
)

func runMediaWorker() error {
	if os.Geteuid() == 0 {
		return errors.New("the media worker must run under its own dynamic identity, never root")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mediaworker.ServeStandardInput(ctx, mediaworker.Tools{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"})
}

func runPhotoService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the photo service must run as its own identity, never root")
	}
	// The system test shortens how long the library must go unused before AI
	// work starts, so that it sees a photo organised; products keep the default.
	var quiet time.Duration
	if value := strings.TrimSpace(os.Getenv("ANAS_PHOTOS_AI_QUIET_PERIOD")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return errors.New("ANAS_PHOTOS_AI_QUIET_PERIOD must be a positive duration such as 5s")
		}
		quiet = parsed
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lookup := sessionlookup.NewClient(environment("ANAS_PHOTO_SESSION_SOCKET", "/run/a-nas-sessions/photos.sock"))
	return photoservice.Run(ctx, photoservice.Config{
		Root:       environment("ANAS_PHOTOS_ROOT", "/srv/a-nas/data/photos"),
		SocketPath: environment("ANAS_PHOTOS_SOCKET", "/run/a-nas-photos/photos.sock"),
		Sessions:   lookup,
		// The AI assistant is not shipped yet (ADR 0022), so its socket is off
		// unless named.
		AssistantSocketPath: os.Getenv("ANAS_PHOTOS_ASSISTANT_SOCKET"),
		Accounts:            lookup,
		Logger:              logger,
		// Without the AI component the socket is absent and photos wait for it.
		AI:            aiworker.Client{SocketPath: environment("ANAS_AI_SOCKET", "/run/a-nas-ai/ai.sock")},
		AIQuietPeriod: quiet,
	})
}

func run(logger *slog.Logger) error {
	address := environment("ANAS_HTTP_ADDR", "127.0.0.1:8080")
	stateDirectory, err := stateDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return err
	}
	reader, dataSource, operations, live, err := configuredServices()
	if err != nil {
		return err
	}
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
	defer accountStore.Close()
	fileStore, err := files.OpenSQLite(filepath.Join(stateDirectory, "files.db"))
	if err != nil {
		return err
	}
	defer fileStore.Close()
	storageStore, err := storage.OpenSQLite(filepath.Join(stateDirectory, "storage.db"))
	if err != nil {
		return err
	}
	defer storageStore.Close()
	accountService := accounts.NewService(accountStore, operations, accounts.Options{})
	storageService, err := storage.OpenService(reader, operations, storage.Options{Store: storageStore})
	if err != nil {
		return err
	}
	volumeRoot := strings.TrimSpace(os.Getenv("ANAS_DATA_MOUNT"))
	if volumeRoot == "" {
		if live {
			volumeRoot = "/srv/a-nas/data"
		} else {
			volumeRoot = filepath.Join(stateDirectory, "development-volume")
		}
	}
	fileOptions := files.Options{AllowUnverifiedVolume: !live}
	terminalConfig := terminal.Config{}
	var broker *filebroker.Client
	if live {
		fileOptions.VolumeGuard = sandboxedVolumeGuard{files.BtrfsVolumeGuard{ExpectedFilesystemUUID: func(ctx context.Context) (string, error) {
			volumes, err := storageService.ListVolumes(ctx)
			if err != nil || len(volumes) != 1 || volumes[0].State != storage.VolumeStateAvailable {
				return "", err
			}
			return volumes[0].FilesystemUUID, nil
		}}}
		fileOptions.SnapshotBackend = operations
		brokerSocket, err := fileBrokerSocket()
		if err != nil {
			return err
		}
		broker = filebroker.NewClient(brokerSocket)
		fileOptions.FileSystem = broker
		// Terminal shells run as the signed-in administrator, not as a-nas.
		terminalConfig.Spawner = broker
	} else if err := os.MkdirAll(volumeRoot, 0o700); err != nil {
		return err
	}
	fileService := files.NewService(fileStore, volumeRoot, accountService, fileOptions)
	// The media center and the music library share one FFmpeg boundary and
	// adapt its operations to their own needs (ADR 0017, ADR 0018).
	mediaWorker, err := configuredMediaWorker(live)
	if err != nil {
		return err
	}
	musicStore, err := music.OpenSQLite(filepath.Join(stateDirectory, "music.db"))
	if err != nil {
		return err
	}
	defer musicStore.Close()
	// Runs before the deferred musicStore.Close, after the server stopped.
	musicService := music.New(musicStore, filepath.Join(stateDirectory, "music-covers"), fileService, accountService, music.Options{Logger: logger, Worker: mediaWorker})
	defer musicService.Close()
	// The Product Service cannot reach the data volume in production; there
	// the photo service owns the photos subvolume under its own identity and
	// the Product Service only forwards the photo API to it (ADR 0011).
	// Development opens the library in this process.
	var photoService *photos.Service
	var photoAI photos.Embedder
	var photoAPI http.Handler = photosapi.NewProxy(environment("ANAS_PHOTOS_SOCKET", "/run/a-nas-photos/photos.sock"), logger)
	if !live {
		photoAI = developmentAI()
		photoService, err = photos.Open(filepath.Join(volumeRoot, "photos"), photos.Options{AI: photoAI})
		if err != nil {
			return err
		}
		defer photoService.Close()
		photoAPI = photosapi.New(photoService, logger)
	}
	mediaStore, err := media.OpenSQLite(filepath.Join(stateDirectory, "media.db"))
	if err != nil {
		return err
	}
	defer mediaStore.Close()
	var processor media.Processor
	if mediaWorker != nil {
		processor = media.WorkerProcessor{Worker: mediaWorker}
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
	defer feedbackStore.Close()
	transferService, err := transfers.Open(filepath.Join(stateDirectory, "transfers.db"), fileService, accountService, transfers.Options{
		Sessions: accountService.RememberedSessions, FilesChanged: mediaService.SpaceChanged, Logger: logger,
	})
	if err != nil {
		return err
	}
	defer transferService.Close()
	transferAPI, err := transfersapi.New(transferService, logger)
	if err != nil {
		return err
	}
	terminalEnabled, err := configuredTerminal()
	if err != nil {
		return err
	}
	terminalConfig.Enabled = terminalEnabled
	terminals := terminal.New(terminalConfig, logger)

	containerManager, appStore, containerSource, err := configuredContainers(dataSource)
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
		guard := fileOptions.VolumeGuard
		appOptions.VolumeReady = func(ctx context.Context) error { return guard.Check(ctx, volumeRoot, false) }
	}

	apiHandler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: reader, DataSource: dataSource, ProductVersion: version,
		Accounts: accountService, Files: fileService, Storage: storageService,
		Terminal:   terminals,
		Containers: containersapi.New(containerManager, containerSource, logger),
		Apps:       appstoreapi.New(appStore, appstoreapi.DataSource(containerSource), appOptions),
		Photos:     photoAPI,
		Media:      mediaapi.New(mediaService, logger),
		Music:      musicService,
		Feedback:   feedbackapi.New(feedbackStore, logger),
		Transfers:  transferAPI,
		Assistant:  assistantAPI(logger, broker),
		SystemLogs: systemLogs,
		// Web uploads and other changes rescan the media libraries of their
		// space a few seconds later.
		FilesChanged: mediaService.SpaceChanged,
		Logger:       logger,
	})
	handler, err := webui.NewWithOptions(apiHandler, webui.Options{
		ScreensaverDirectory: configuredScreensaverDirectory(),
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go syncIdentities(ctx, accountService, logger)
	go runDirectoryReconciliation(ctx, accountService, fileService, logger)
	// The Host Agent takes scheduled snapshots, so they need no session.
	go fileService.RunSnapshotSchedules(ctx, accountService, logger)
	go mediaService.Run(ctx)
	go transferService.Run(ctx)
	if photoService != nil {
		background := make(chan struct{})
		go func() {
			defer close(background)
			photoservice.RunBackground(ctx, photoService, logger, photoAI, photoservice.NewGate(photoservice.QuietPeriod, logger))
		}()
		// Runs before the deferred photoService.Close.
		defer func() {
			stop()
			<-background
		}()
	}
	listenError := make(chan error, 1)
	go func() {
		logger.Info("A-NAS API listening", "address", address, "version", version, "data_source", dataSource, "terminal", terminalEnabled, "containers", containerManager != nil)
		listenError <- server.ListenAndServe()
	}()
	select {
	case err := <-listenError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("A-NAS API shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := terminals.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-listenError
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
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

// configuredMediaWorker picks where FFmpeg runs. Production uses the
// sandboxed worker socket; development runs FFmpeg in this process when it
// is installed, and otherwise the media center and music work without it.
func configuredMediaWorker(live bool) (mediaworker.Worker, error) {
	mode := os.Getenv("ANAS_MEDIA_WORKER")
	if mode == "" {
		mode = "local"
		if live {
			mode = "socket"
		}
	}
	switch mode {
	case "socket":
		return mediaworker.Client{Socket: environment("ANAS_MEDIA_SOCKET", "/run/a-nas-media/media.sock")}, nil
	case "local":
		if tools, ok := mediaworker.LocalTools(); ok {
			return mediaworker.Local{Tools: tools}, nil
		}
		return nil, nil
	case "disabled":
		return nil, nil
	}
	return nil, errors.New("ANAS_MEDIA_WORKER must be socket, local or disabled")
}

// developmentAI reaches an AI Worker only when ANAS_AI_SOCKET names one, so
// development without the AI component keeps local AI off.
func developmentAI() photos.Embedder {
	if socket := os.Getenv("ANAS_AI_SOCKET"); socket != "" {
		return aiworker.Client{SocketPath: socket}
	}
	return nil
}

// syncIdentities converges host accounts on the control plane at startup, so
// accounts created before ADR 0008 receive their A-NAS UID and groups.
func syncIdentities(ctx context.Context, accountService *accounts.Service, logger *slog.Logger) {
	syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := accountService.SyncIdentities(syncCtx); err != nil {
		logger.ErrorContext(ctx, "identity synchronization failed", "error", err)
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

func configuredServices() (hoststate.Observer, httpapi.DataSource, productOperations, bool, error) {
	switch mode := os.Getenv("ANAS_HOSTSTATE_MODE"); mode {
	case "", "fake":
		operations := &developmentOperations{}
		return fake.NewHealthy(), httpapi.DataSourceSimulated, operations, false, nil
	case "agent":
		socketPath, err := agent.SocketPath()
		if err != nil {
			return nil, "", nil, false, err
		}
		client := agent.NewClient(socketPath)
		return client, httpapi.DataSourceLive, client, true, nil
	default:
		return nil, "", nil, false, errors.New("ANAS_HOSTSTATE_MODE must be fake or agent")
	}
}

// fileBrokerSocket defaults to the File Broker socket beside the Host Agent's.
func fileBrokerSocket() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ANAS_FILE_BROKER_SOCKET")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("ANAS_FILE_BROKER_SOCKET must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	socketPath, err := agent.SocketPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(socketPath), "file-broker.sock"), nil
}

func stateDirectory() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ANAS_STATE_DIR")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("ANAS_STATE_DIR must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "a-nas", "state"), nil
}

func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func configuredScreensaverDirectory() string {
	if directory := strings.TrimSpace(os.Getenv("ANAS_SCREENSAVER_DIRECTORY")); directory != "" {
		return directory
	}
	if legacyVideo := strings.TrimSpace(os.Getenv("ANAS_SCREENSAVER_VIDEO")); legacyVideo != "" {
		return filepath.Dir(legacyVideo)
	}
	return "/var/lib/a-nas/screensavers/current"
}

type developmentOperations struct{}

func (*developmentOperations) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (*developmentOperations) DisableCredential(context.Context, string) error { return nil }

// Development volumes have no ACLs; viewing access is decided by the Policy.
func (*developmentOperations) GrantViewing(context.Context, accounts.ViewingRequest) error {
	return nil
}
func (*developmentOperations) RevokeViewing(context.Context, string) error { return nil }
func (*developmentOperations) CreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	return storage.Volume{ID: "volume:data", DiskID: request.DiskID, FilesystemUUID: "development", State: storage.VolumeStateAvailable}, nil
}
func (*developmentOperations) Create(context.Context, string, string) ([]files.SnapshotObject, error) {
	return nil, errors.New("development snapshot backend is not configured")
}
func (*developmentOperations) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return nil, files.SnapshotObject{}, files.ErrNotFound
}
func (*developmentOperations) Delete(context.Context, string, string) error { return files.ErrNotFound }

func configuredTerminal() (bool, error) {
	switch mode := os.Getenv("ANAS_TERMINAL"); mode {
	case "", "disabled":
		return false, nil
	case "enabled":
		return true, nil
	default:
		return false, errors.New("ANAS_TERMINAL must be enabled or disabled")
	}
}

// configuredContainers selects container management and the App Center
// together, since both run through the container agent. It defaults to the
// Fake Adapters only alongside simulated host state, so a live deployment
// never shows invented containers or apps.
func configuredContainers(hostSource httpapi.DataSource) (containers.Manager, appstore.Store, containersapi.DataSource, error) {
	mode := os.Getenv("ANAS_CONTAINERS_MODE")
	if mode == "" {
		mode = "disabled"
		if hostSource == httpapi.DataSourceSimulated {
			mode = "fake"
		}
	}
	switch mode {
	case "fake":
		apps, err := fakeappstore.New(700 * time.Millisecond)
		if err != nil {
			return nil, nil, "", err
		}
		return fakecontainers.New(), apps, containersapi.DataSourceSimulated, nil
	case "agent":
		socketPath, err := containeragent.SocketPath()
		if err != nil {
			return nil, nil, "", err
		}
		return containeragent.NewClient(socketPath), appstoreagent.NewClient(socketPath), containersapi.DataSourceLive, nil
	case "disabled":
		return nil, nil, containersapi.DataSourceLive, nil
	default:
		return nil, nil, "", errors.New("ANAS_CONTAINERS_MODE must be fake, agent or disabled")
	}
}

// assistantAPI configures the AI assistant (ADR 0022). Without
// ANAS_ASSISTANT_SOCKET_DIR it answers assistant_unavailable and the desktop
// keeps the assistant as planned. With the File Broker every connection to a
// member's instance goes through it, the session gate (decision 10); without
// it, in development only, the sockets are dialled directly with the tokens in
// ANAS_ASSISTANT_TOKEN_DIR. The File Broker also executes the assistant's
// changes; ANAS_ASSISTANT_CHANGES_SOCKET points at the spike's change broker
// instead, which stands in for it where there is no File Broker.
func assistantAPI(logger *slog.Logger, broker *filebroker.Client) http.Handler {
	sockets := strings.TrimSpace(os.Getenv("ANAS_ASSISTANT_SOCKET_DIR"))
	options := assistantapi.Options{SocketDir: sockets, Logger: logger}
	switch {
	case sockets == "":
	case broker != nil:
		options.Connector = gatedAssistant{broker}
	default:
		options.Connector = assistantapi.SocketConnector{SocketDir: sockets, TokenDir: os.Getenv("ANAS_ASSISTANT_TOKEN_DIR")}
	}
	if socket := os.Getenv("ANAS_ASSISTANT_CHANGES_SOCKET"); socket != "" {
		options.Changes = assistantapi.BrokerChanges(socket)
	} else if broker != nil {
		options.Changes = executedChanges{broker}
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

func (g gatedAssistant) Connect(ctx context.Context, account string) (net.Conn, string, error) {
	conn, err := g.broker.ConnectAssistant(ctx)
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
