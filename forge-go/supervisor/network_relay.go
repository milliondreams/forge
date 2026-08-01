//go:build !windows

package supervisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const agentNetworkRelayRoot = "/run/forge-agent-network"

// AgentNetworkRelay is a per-agent HTTP/CONNECT proxy bound to a private Unix
// socket. The sandbox has no external network namespace; an in-sandbox helper
// forwards its loopback proxy port to this socket.
type AgentNetworkRelay struct {
	socketPath string
	allowed    []string
	upstream   *url.URL
	listener   net.Listener
	server     *http.Server
	closeOnce  sync.Once
}

func NewAgentNetworkRelay(root, key string, allowed []string) (*AgentNetworkRelay, error) {
	if root == "" {
		root = agentNetworkRelayRoot
	}
	socketPath := agentNetworkRelaySocketPath(root, key)
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create agent network relay directory: %w", err)
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on agent network relay: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("protect agent network relay socket: %w", err)
	}
	relay := &AgentNetworkRelay{
		socketPath: socketPath,
		allowed:    append([]string(nil), allowed...),
		listener:   listener,
	}
	relay.upstream = configuredUpstreamProxy()
	relay.server = &http.Server{
		Handler:           relay,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() { _ = relay.server.Serve(listener) }()
	return relay, nil
}

func agentNetworkRelaySocketPath(root, key string) string {
	sum := sha256.Sum256([]byte(key))
	dir := filepath.Join(root, hex.EncodeToString(sum[:8]))
	return filepath.Join(dir, "proxy.sock")
}

func configuredUpstreamProxy() *url.URL {
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
				return parsed
			}
		}
	}
	return nil
}

func agentOSManagerDestination(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse manager API URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("manager API URL must be an HTTP loopback origin")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("manager API URL must use a loopback host")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	return net.JoinHostPort(host, port), nil
}

func (r *AgentNetworkRelay) SocketPath() string { return r.socketPath }

func (r *AgentNetworkRelay) Close() error {
	var result error
	r.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		result = r.server.Shutdown(ctx)
		_ = r.listener.Close()
		_ = os.Remove(r.socketPath)
		_ = os.Remove(filepath.Dir(r.socketPath))
	})
	return result
}

func (r *AgentNetworkRelay) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	hostPort := request.URL.Host
	if request.Method == http.MethodConnect {
		hostPort = request.Host
	}
	host, port, err := splitDestination(hostPort, request.Method == http.MethodConnect)
	if err != nil || !r.destinationAllowed(host, port) {
		http.Error(w, "destination denied by agent network policy", http.StatusForbidden)
		return
	}
	if request.Method == http.MethodConnect {
		r.serveConnect(w, request, net.JoinHostPort(host, port))
		return
	}

	clone := request.Clone(request.Context())
	clone.RequestURI = ""
	clone.Header.Del("Proxy-Authorization")
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			if isAgentOSRelayLocalService(host) {
				return nil, nil
			}
			if r.upstream == nil {
				return nil, fmt.Errorf("host egress proxy is not configured")
			}
			return r.upstream, nil
		},
		DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	response, err := transport.RoundTrip(clone)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (r *AgentNetworkRelay) serveConnect(w http.ResponseWriter, request *http.Request, target string) {
	upstream, err := r.dialConnect(request.Context(), target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "proxy transport does not support tunneling", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if buffered.Reader.Buffered() > 0 {
		_, _ = io.CopyN(upstream, buffered, int64(buffered.Reader.Buffered()))
	}
	go func() {
		defer client.Close()
		defer upstream.Close()
		_, _ = io.Copy(upstream, client)
	}()
	go func() {
		defer client.Close()
		defer upstream.Close()
		_, _ = io.Copy(client, upstream)
	}()
}

func (r *AgentNetworkRelay) dialConnect(ctx context.Context, target string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if isAgentOSRelayLocalTarget(target) {
		return dialer.DialContext(ctx, "tcp", target)
	}
	if r.upstream == nil {
		return nil, fmt.Errorf("host egress proxy is not configured")
	}
	conn, err := dialer.DialContext(ctx, "tcp", r.upstream.Host)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("upstream proxy denied CONNECT: %s", response.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func splitDestination(value string, connect bool) (string, string, error) {
	host, port, err := net.SplitHostPort(value)
	if err == nil {
		return strings.ToLower(strings.TrimSuffix(host, ".")), port, nil
	}
	if strings.Contains(value, ":") {
		return "", "", err
	}
	port = "80"
	if connect {
		port = "443"
	}
	return strings.ToLower(strings.TrimSuffix(value, ".")), port, nil
}

func (r *AgentNetworkRelay) destinationAllowed(host, port string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && !isAgentOSRelayLocalService(host) {
		return false
	}
	for _, rule := range r.allowed {
		rule = strings.TrimSpace(strings.ToLower(rule))
		if rule == "" || rule == "none" {
			continue
		}
		if parsed, err := url.Parse(rule); err == nil && parsed.Host != "" {
			rule = parsed.Host
		}
		ruleHost, rulePort, err := net.SplitHostPort(rule)
		if err == nil {
			if strings.EqualFold(ruleHost, host) && rulePort == port {
				return true
			}
			continue
		}
		if strings.HasPrefix(rule, "*.") {
			suffix := strings.TrimPrefix(rule, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
				return true
			}
			continue
		}
		if rule == host {
			return true
		}
	}
	return false
}

func isAgentOSLocalService(host string) bool {
	return host == "10.0.2.101"
}

func isAgentOSRelayLocalService(host string) bool {
	if isAgentOSLocalService(host) || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isAgentOSRelayLocalTarget(target string) bool {
	host, _, err := net.SplitHostPort(target)
	return err == nil && isAgentOSRelayLocalService(host)
}
