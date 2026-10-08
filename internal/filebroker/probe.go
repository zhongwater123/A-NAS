package filebroker

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProbeArgument is the Host Agent subcommand that reports the identity it
// runs under, so the broker can prove at startup that it can start workers.
const ProbeArgument = "file-worker-probe"

// probeID is nobody on Debian: the probe proves the identity switch without
// touching any A-NAS account or data.
const probeID = 65534

// RunProbe prints the real UID, GID and supplementary groups it runs with.
func RunProbe() error {
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	ids := make([]string, len(groups))
	for i, group := range groups {
		ids[i] = strconv.Itoa(group)
	}
	_, err = fmt.Printf("%d %d %s\n", os.Getuid(), os.Getgid(), strings.Join(ids, ","))
	return err
}

// VerifyIdentitySwitch starts the probe the way it starts a file worker, but
// as nobody, and checks that the probe really runs as nobody. A Host Agent
// whose service sandbox took CAP_SETUID or CAP_SETGID (issue #38) fails here
// at startup instead of on every Web file request.
func (s *Server) VerifyIdentitySwitch() error {
	cmd := s.config.ProbeCommand()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.SysProcAttr = launchAttributes(&syscall.Credential{Uid: probeID, Gid: probeID, Groups: []uint32{probeID}})
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start probe as UID %d: %w", probeID, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("probe as UID %d: %w", probeID, err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("identity probe did not finish")
	}
	want := fmt.Sprintf("%d %d %d", probeID, probeID, probeID)
	if got := strings.TrimSpace(output.String()); got != want {
		return fmt.Errorf("identity probe ran as %q, want %q", got, want)
	}
	return nil
}

// launchAttributes are shared by workers and the probe, so the probe
// exercises exactly the switch a worker needs.
func launchAttributes(credential *syscall.Credential) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL, Credential: credential}
}
