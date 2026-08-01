//go:build !windows

package supervisor

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

func TestAgentOSRedisRelayEndpointsRequireLoopback(t *testing.T) {
	for _, test := range []struct {
		value      string
		wantTarget string
		wantListen string
	}{
		{"127.0.0.1:6379", "127.0.0.1:6379", "127.0.0.1:6379"},
		{"localhost:6380", "127.0.0.1:6380", "127.0.0.1:6380"},
		{"[::1]:6381", "[::1]:6381", "127.0.0.1:6381"},
		{"redis://127.0.0.1:6382", "127.0.0.1:6382", "127.0.0.1:6382"},
	} {
		target, listen, err := agentOSRedisRelayEndpoints(test.value)
		if err != nil || target != test.wantTarget || listen != test.wantListen {
			t.Fatalf("agentOSRedisRelayEndpoints(%q) = %q, %q, %v; want %q, %q", test.value, target, listen, err, test.wantTarget, test.wantListen)
		}
	}

	for _, value := range []string{"", "10.0.2.2:6379", "redis.example.com:6379", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:redis", "redis://user@127.0.0.1:6379", "rediss://127.0.0.1:6379"} {
		if _, _, err := agentOSRedisRelayEndpoints(value); err == nil {
			t.Fatalf("agentOSRedisRelayEndpoints(%q) unexpectedly succeeded", value)
		}
	}
}

func TestAgentOSRedisRelayIsRestrictedToSystemAgents(t *testing.T) {
	sup := NewBubblewrapSupervisor(nil,
		WithBubblewrapAgentOSMode(true),
		WithBubblewrapSystemRedisAddress("127.0.0.1:6379"),
	)
	system := &protocol.AgentSpec{ClassName: "rustic_ai.forge.agents.system.guild_manager_agent.GuildManagerAgent"}
	target, listen, err := sup.agentOSSystemRedisEndpoints(system)
	if err != nil || target != "127.0.0.1:6379" || listen != "127.0.0.1:6379" {
		t.Fatalf("system Redis endpoints = %q, %q, %v", target, listen, err)
	}

	user := &protocol.AgentSpec{ClassName: "example.UserAgent"}
	target, listen, err = sup.agentOSSystemRedisEndpoints(user)
	if err != nil || target != "" || listen != "" {
		t.Fatalf("user agent received Redis endpoints = %q, %q, %v", target, listen, err)
	}
}

func TestAgentTCPRelayRoundTripAndCleanup(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		connection, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = io.Copy(connection, connection)
	}()

	tempRoot := t.TempDir()
	shortRoot := filepath.Join("/tmp", filepath.Base(tempRoot))
	if err := os.Symlink(tempRoot, shortRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(shortRoot) })
	relay, err := NewAgentTCPRelay(shortRoot, "guild/manager", upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("unix", relay.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "ping" {
		t.Fatalf("relay response = %q", response)
	}
	connection.Close()

	socketPath := relay.SocketPath()
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("relay socket still exists after close: %v", err)
	}
}

func TestAgentOSBubblewrapMountsSharedRelayDirectoryOnce(t *testing.T) {
	root := t.TempDir()
	httpRelay := &AgentNetworkRelay{socketPath: agentNetworkRelaySocketPath(root, "guild/manager")}
	tcpRelay := &AgentTCPRelay{socketPath: agentTCPRelaySocketPath(root, "guild/manager")}
	sup := NewBubblewrapSupervisor(nil, WithBubblewrapAgentOSMode(true))
	args := sup.buildBwrapArgsWithRelays(
		&registry.AgentRegistryEntry{Network: []string{"none"}},
		[]string{"python", "-m", "agent"}, nil, httpRelay, tcpRelay, nil, nil,
	)

	dir := filepath.Dir(httpRelay.SocketPath())
	count := 0
	for index := range args {
		if args[index] == dir {
			count++
		}
		if args[index] == "--share-net" {
			t.Fatalf("AgentOS relay sandbox must not share the guest network: %v", args)
		}
	}
	if count != 2 {
		t.Fatalf("shared relay directory should appear in one ro-bind pair, got %d occurrences in %v", count, args)
	}
}
