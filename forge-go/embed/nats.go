package embed

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/rustic-ai/forge/forge-go/forgepath"
)

// EmbeddedNATS wraps an in-process NATS server with JetStream enabled.
type EmbeddedNATS struct {
	server       *natsserver.Server
	storeDir     string
	cleanupStore bool
}

// StartEmbeddedNATS spins up a new in-process NATS server on an ephemeral port.
func StartEmbeddedNATS() (*EmbeddedNATS, error) {
	return StartEmbeddedNATSAt("")
}

// StartEmbeddedNATSAt spins up a new in-process NATS server on a specific address.
// If addr is empty, an ephemeral port is used.
func StartEmbeddedNATSAt(addr string) (*EmbeddedNATS, error) {
	storeDir, err := os.MkdirTemp("", "forge-embedded-nats-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary embedded NATS store dir: %w", err)
	}
	return startEmbeddedNATSAt(addr, storeDir, true, 15*time.Second)
}

// StartPersistentEmbeddedNATSAt spins up an in-process NATS server whose
// JetStream store is preserved under FORGE_HOME across server restarts.
func StartPersistentEmbeddedNATSAt(addr string) (*EmbeddedNATS, error) {
	return startEmbeddedNATSAt(addr, forgepath.Resolve("nats"), false, 15*time.Second)
}

func startEmbeddedNATSAt(addr, storeDir string, cleanupStore bool, readyTimeout time.Duration) (*EmbeddedNATS, error) {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		if cleanupStore {
			_ = os.RemoveAll(storeDir)
		}
		return nil, fmt.Errorf("failed to create embedded NATS store dir %s: %w", storeDir, err)
	}

	cleanupOnError := func() {
		if cleanupStore {
			_ = os.RemoveAll(storeDir)
		}
	}

	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  storeDir,
		Port:      -1,
		// Forge owns process signals. The embedded NATS server must not install
		// its standalone SIGTERM handler, which calls os.Exit(0) and would abort
		// Forge while it is still draining owned agent workloads.
		NoSigs: true,
	}

	addr = strings.TrimSpace(addr)
	if addr != "" {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("invalid port in address %q: %w", addr, err)
		}
		opts.Host = host
		opts.Port = port
	}

	s, err := natsserver.NewServer(opts)
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("failed to create embedded NATS server: %w", err)
	}

	go s.Start()

	if !s.ReadyForConnections(readyTimeout) {
		s.Shutdown()
		s.WaitForShutdown()
		cleanupOnError()
		return nil, fmt.Errorf("embedded NATS server did not become ready within %s", readyTimeout)
	}

	return &EmbeddedNATS{server: s, storeDir: storeDir, cleanupStore: cleanupStore}, nil
}

// Host returns the bound hostname.
func (e *EmbeddedNATS) Host() string {
	return e.server.Addr().(*net.TCPAddr).IP.String()
}

// Port returns the bound port.
func (e *EmbeddedNATS) Port() int {
	return e.server.Addr().(*net.TCPAddr).Port
}

// Addr returns host:port.
func (e *EmbeddedNATS) Addr() string {
	return fmt.Sprintf("%s:%d", e.Host(), e.Port())
}

// ClientURL returns the NATS client URL (nats://host:port).
func (e *EmbeddedNATS) ClientURL() string {
	return e.server.ClientURL()
}

// Client returns a new nats.Conn connected to this instance. The caller is
// responsible for closing the connection.
func (e *EmbeddedNATS) Client() (*nats.Conn, error) {
	return nats.Connect(e.ClientURL())
}

// Close shuts down the embedded server, waits for JetStream to flush, and
// removes the store only for explicitly temporary instances.
func (e *EmbeddedNATS) Close() {
	if e.server != nil {
		e.server.Shutdown()
		e.server.WaitForShutdown()
	}
	if e.cleanupStore && e.storeDir != "" {
		_ = os.RemoveAll(filepath.Clean(e.storeDir))
	}
}
