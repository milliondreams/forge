//go:build !windows

package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AgentTCPRelay exposes one fixed loopback control-plane service over a private
// Unix socket. It is intentionally not a general-purpose network proxy.
type AgentTCPRelay struct {
	socketPath string
	target     string
	listener   net.Listener

	mu        sync.Mutex
	active    map[net.Conn]struct{}
	closed    bool
	closeOnce sync.Once
}

func NewAgentTCPRelay(root, key, target string) (*AgentTCPRelay, error) {
	normalizedTarget, _, err := agentOSRedisRelayEndpoints(target)
	if err != nil {
		return nil, err
	}
	if root == "" {
		root = agentNetworkRelayRoot
	}
	socketPath := agentTCPRelaySocketPath(root, key)
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create agent TCP relay directory: %w", err)
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on agent TCP relay: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("protect agent TCP relay socket: %w", err)
	}
	relay := &AgentTCPRelay{
		socketPath: socketPath,
		target:     normalizedTarget,
		listener:   listener,
		active:     make(map[net.Conn]struct{}),
	}
	go relay.serve()
	return relay, nil
}

func agentTCPRelaySocketPath(root, key string) string {
	sum := sha256.Sum256([]byte(key))
	dir := filepath.Join(root, hex.EncodeToString(sum[:8]))
	return filepath.Join(dir, "redis.sock")
}

func agentOSRedisRelayEndpoints(address string) (target, listen string, err error) {
	value := strings.TrimSpace(address)
	if strings.Contains(value, "://") {
		parsed, parseErr := url.Parse(value)
		if parseErr != nil || parsed.Scheme != "redis" || parsed.User != nil || parsed.Host == "" ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", fmt.Errorf("Redis control endpoint must be a loopback host and port")
		}
		value = parsed.Host
	}
	host, port, splitErr := net.SplitHostPort(value)
	portNumber, portErr := strconv.Atoi(port)
	if splitErr != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return "", "", fmt.Errorf("Redis control endpoint must be a loopback host and port")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", "", fmt.Errorf("Redis control endpoint must use a loopback host")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), net.JoinHostPort("127.0.0.1", port), nil
}

func (r *AgentTCPRelay) SocketPath() string { return r.socketPath }

func (r *AgentTCPRelay) serve() {
	for {
		client, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.forward(client)
	}
}

func (r *AgentTCPRelay) forward(client net.Conn) {
	upstream, err := net.DialTimeout("tcp", r.target, 10*time.Second)
	if err != nil {
		client.Close()
		return
	}
	if !r.track(client, upstream) {
		client.Close()
		upstream.Close()
		return
	}
	defer r.untrack(client, upstream)
	defer client.Close()
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
}

func (r *AgentTCPRelay) track(connections ...net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	for _, connection := range connections {
		r.active[connection] = struct{}{}
	}
	return true
}

func (r *AgentTCPRelay) untrack(connections ...net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, connection := range connections {
		delete(r.active, connection)
	}
}

func (r *AgentTCPRelay) Close() error {
	var result error
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		for connection := range r.active {
			_ = connection.Close()
		}
		r.active = make(map[net.Conn]struct{})
		r.mu.Unlock()
		if r.listener != nil {
			result = r.listener.Close()
		}
		_ = os.Remove(r.socketPath)
		_ = os.Remove(filepath.Dir(r.socketPath))
	})
	return result
}
