// Package restart implements zero-downtime binary restart via Unix
// socket file descriptor handoff. The parent process exports its
// listening sockets as file descriptors, re-execs itself, and the
// child inherits those FDs — eliminating the port-in-use race and
// serving gap. This pattern requires Linux or Darwin.
package restart

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ListenerDesc describes one live listener for the handoff. Name is
// the listener's config name (unique). Protocol identifies the listener
// type for reconstruction: "tcp", "udp", or "http". File is the
// underlying os.File obtained from the listener via the appropriate
// File() method. The caller must duplicate the FD so the original
// listener remains usable after handoff returns.
type ListenerDesc struct {
	Name     string
	Protocol string
	File     *os.File
}

const (
	envHandoff     = "HOOT_LB_HANDOFF"
	envFDMap       = "HOOT_LB_FD_MAP"
	envConfig      = "HOOT_LB_CONFIG_PATH"
	envReadyFD     = "HOOT_LB_READY_FD"
	handoffTimeout = 15 * time.Second
)

var inProgress sync.Mutex

// IsHandoff reports whether the current process was started as a
// handoff child with inherited listener FDs.
func IsHandoff() bool {
	return os.Getenv(envHandoff) == "1"
}

// ConfigPath returns the config file path passed through the handoff
// environment, or the empty string if this is not a handoff process.
func ConfigPath() string {
	return os.Getenv(envConfig)
}

// ErrHandoffInProgress is returned when a second trigger arrives
// while a handoff is already in progress.
var ErrHandoffInProgress = fmt.Errorf("restart: handoff already in progress")

// ErrUnsupported is returned on platforms that do not support FD
// passing via fork/exec.
var ErrUnsupported = fmt.Errorf("restart: not supported on this OS")

// Trigger re-execs the current process, passing the given listener
// file descriptors to the child. It blocks until the child signals
// readiness or the handoff times out. On success Trigger calls
// os.Exit — the caller must not proceed. On failure Trigger returns
// an error and the caller continues running normally.
//
// Exactly one Trigger call may be in progress at a time. A second
// concurrent call returns ErrHandoffInProgress immediately.
func Trigger(listeners []ListenerDesc, configPath string, logger *slog.Logger) error {
	if !inProgress.TryLock() {
		return ErrHandoffInProgress
	}
	defer inProgress.Unlock()

	if len(listeners) == 0 {
		return fmt.Errorf("restart: no listeners to hand off")
	}

	sorted := make([]ListenerDesc, len(listeners))
	copy(sorted, listeners)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	extraFiles := make([]*os.File, len(sorted))
	for i, l := range sorted {
		extraFiles[i] = l.File
	}

	fdPairs := make([]string, len(sorted))
	for i, l := range sorted {
		fdPairs[i] = l.Name + ":" + strconv.Itoa(i)
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("restart: creating readiness pipe: %w", err)
	}
	defer readyR.Close()
	defer readyW.Close()

	extraFiles = append(extraFiles, readyW)
	readyFDNum := len(extraFiles) - 1 + 3

	env := os.Environ()
	env = append(env,
		envHandoff+"=1",
		envFDMap+"="+strings.Join(fdPairs, ","),
		envConfig+"="+configPath,
		envReadyFD+"="+strconv.Itoa(readyFDNum),
	)

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: resolving executable: %w", err)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.ExtraFiles = extraFiles
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil

	logger.Info("zero-downtime restart initiated",
		slog.String("component", "restart"),
		slog.Int("listeners", len(sorted)))

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart: starting child: %w", err)
	}

	for _, l := range sorted {
		l.File.Close()
	}

	logger.Info("waiting for child readiness",
		slog.String("component", "restart"),
		slog.Duration("timeout", handoffTimeout))

	timer := time.NewTimer(handoffTimeout)
	defer timer.Stop()

	buf := make([]byte, 1)
	readyCh := make(chan error, 1)
	go func() {
		n, err := readyR.Read(buf)
		if n == 0 && err != nil {
			readyCh <- err
		} else {
			readyCh <- nil
		}
	}()

	select {
	case readErr := <-readyCh:
		if readErr != nil {
			logger.Error("child readiness read failed",
				slog.String("component", "restart"),
				slog.String("error", readErr.Error()))
			cmd.Process.Kill()
			cmd.Wait()
			return fmt.Errorf("restart: readiness pipe: %w", readErr)
		}
		if buf[0] != 'R' {
			logger.Error("unexpected readiness byte",
				slog.String("component", "restart"),
				slog.String("byte", string(buf[0])))
			cmd.Process.Kill()
			cmd.Wait()
			return fmt.Errorf("restart: unexpected readiness byte: %q", buf[0])
		}
		logger.Info("child ready, parent will drain and exit",
			slog.String("component", "restart"))
		return nil
	case <-timer.C:
		logger.Error("child readiness timeout",
			slog.String("component", "restart"),
			slog.Duration("timeout", handoffTimeout))
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("restart: child not ready within %s", handoffTimeout)
	}
}

// ReconstructListeners reads the inherited FD environment and returns
// the listener descriptors plus the readiness pipe write end. Returns
// nil if this is not a handoff process.
func ReconstructListeners() ([]ListenerDesc, *os.File) {
	if !IsHandoff() {
		return nil, nil
	}

	fdMapStr := os.Getenv(envFDMap)
	if fdMapStr == "" {
		return nil, nil
	}

	readyFDStr := os.Getenv(envReadyFD)
	readyFDNum, err := strconv.Atoi(readyFDStr)
	if err != nil {
		slog.Error("restart: invalid ready FD",
			slog.String("component", "restart"),
			slog.String("error", err.Error()))
		return nil, nil
	}
	readyW := os.NewFile(uintptr(readyFDNum), "ready-pipe")

	pairs := strings.Split(fdMapStr, ",")
	descs := make([]ListenerDesc, 0, len(pairs))
	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		fdIdx, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		fdNum := fdIdx + 3
		f := os.NewFile(uintptr(fdNum), "listener:"+name)
		if f == nil {
			slog.Error("restart: failed to create file from FD",
				slog.String("component", "restart"),
				slog.String("listener", name),
				slog.Int("fd", fdNum))
			continue
		}
		descs = append(descs, ListenerDesc{Name: name, File: f})
	}

	return descs, readyW
}

// SignalReady writes the readiness byte to the pipe, telling the
// parent that this child has successfully started.
func SignalReady(readyW *os.File) {
	if readyW == nil {
		return
	}
	if _, err := readyW.Write([]byte("R")); err != nil {
		slog.Error("restart: failed to signal readiness",
			slog.String("component", "restart"),
			slog.String("error", err.Error()))
	}
	readyW.Close()
}

// ReconstructTCPListener creates a net.Listener from an inherited FD.
func ReconstructTCPListener(desc ListenerDesc) (net.Listener, error) {
	ln, err := net.FileListener(desc.File)
	desc.File.Close()
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// ReconstructUDPConn creates a *net.UDPConn from an inherited FD.
func ReconstructUDPConn(desc ListenerDesc) (*net.UDPConn, error) {
	pc, err := net.FilePacketConn(desc.File)
	desc.File.Close()
	if err != nil {
		return nil, fmt.Errorf("reconstructing UDP conn: %w", err)
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("packet conn is %T, not *net.UDPConn", pc)
	}
	return uc, nil
}
