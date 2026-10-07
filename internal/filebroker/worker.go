package filebroker

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/files"
)

// WorkerArgument is the Host Agent subcommand that runs a file worker.
const WorkerArgument = "file-worker"

// workerDescriptor is the socket the broker passes as the first extra file.
const workerDescriptor = 3

// RunWorker serves file operations for the broker until it disconnects. It
// runs with the signed-in user's credentials, set by the broker before exec,
// and refuses to run as root.
func RunWorker(volumeRoot string) error {
	if os.Geteuid() == 0 || os.Getegid() == 0 {
		return errors.New("refusing to run a file worker as root")
	}
	syscall.Umask(0o007)
	file := os.NewFile(workerDescriptor, "file-broker")
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return errors.New("file worker descriptor is not a Unix socket")
	}
	defer conn.Close()
	return serveWorker(context.Background(), conn, files.NewRootFileSystem(volumeRoot, false))
}

func serveWorker(ctx context.Context, conn *net.UnixConn, fsys files.FileSystem) error {
	for {
		var req request
		extra, err := readMessage(conn, &req)
		closeFile(extra)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		result, file := execute(ctx, fsys, req)
		err = writeMessage(conn, result, file)
		closeFile(file)
		if err != nil {
			return err
		}
	}
}
