package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Hoot-Code/Hoot-Lb/internal/restart"
)

// proxy is the common lifecycle interface implemented by each
// listener's running proxy (TCP, UDP, HTTP, TLS passthrough), letting
// run() shut all of them down uniformly regardless of protocol.
type proxy interface {
	Close(ctx context.Context) error
	Name() string
}

// listenerRef records how to obtain a duplicated file descriptor for
// one currently-running listener (a proxy listener, or the metrics/
// admin listener), so a future zero-downtime restart can hand it off
// to the child process.
type listenerRef struct {
	name     string
	protocol string
	fileFn   func() (*os.File, error)
}

// currentListeners is the process-wide registry of listeners eligible
// for FD handoff on restart. It is populated as each listener starts
// up in run() and consumed by triggerRestart.
var currentListeners []listenerRef

// proxyAdapter wraps a closeFn/name pair to satisfy the proxy
// interface for listener types that don't have a dedicated wrapper.
type proxyAdapter struct {
	closeFn func(ctx context.Context) error
	name    string
}

func (p *proxyAdapter) Close(ctx context.Context) error { return p.closeFn(ctx) }
func (p *proxyAdapter) Name() string                    { return p.name }

// waitForSignal blocks until either the process receives SIGINT/SIGTERM
// or a successful zero-downtime restart signals restartCh, whichever
// comes first.
func waitForSignal(restartCh <-chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case <-sigCh:
	case <-restartCh:
	}
}

// triggerRestart gathers file descriptors for every currently
// registered listener and hands them off to a re-exec'd child process
// via restart.Trigger, enabling a zero-downtime binary upgrade.
func triggerRestart(configPath string, logger *slog.Logger) error {
	if !restart.Supported() {
		return restart.ErrUnsupported
	}
	var descs []restart.ListenerDesc
	for _, l := range currentListeners {
		f, err := l.fileFn()
		if err != nil {
			continue
		}
		descs = append(descs, restart.ListenerDesc{Name: l.name, Protocol: l.protocol, File: f})
	}
	return restart.Trigger(descs, configPath, logger)
}
