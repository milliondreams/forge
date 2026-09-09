package supervisor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

func TestRemapUVRuntimePathsForContainer(t *testing.T) {
	env := []string{
		"FOO=bar",
		"UV_CACHE_DIR=/host/uv-cache",
		"UV_PYTHON_INSTALL_DIR=/host/python",
		"UV_MANAGED_PYTHON=1",
	}

	gotEnv, gotHostCacheDir := remapUVRuntimePathsForContainer(env)
	wantEnv := []string{
		"FOO=bar",
		"UV_CACHE_DIR=/tmp/forge-uv-cache",
		"UV_PYTHON_INSTALL_DIR=/tmp/forge-python",
		"UV_MANAGED_PYTHON=1",
	}

	if gotHostCacheDir != "/host/uv-cache" {
		t.Fatalf("expected host UV cache directory to be preserved, got %q", gotHostCacheDir)
	}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("unexpected remapped environment:\nwant: %v\n got: %v", wantEnv, gotEnv)
	}
}

func TestBuildContainerConfig_Airgapped(t *testing.T) {
	numCPUs := 2.5
	agentSpec := &protocol.AgentSpec{
		ID: "test-agent-123",
		Resources: protocol.ResourceSpec{
			NumCPUs: &numCPUs,
		},
	}

	entry := &registry.AgentRegistryEntry{
		Network: []string{"none"},
		Filesystem: []registry.FilesystemPermission{
			{Path: "/data", Mode: "rw"},
		},
	}

	cmd := []string{"python", "main.py"}
	env := []string{"FOO=BAR"}

	cCfg, hCfg := BuildContainerConfig(agentSpec, entry, "test-guild", "ghcr.io/astral-sh/uv:python3.13-bookworm-slim", cmd, env)

	// Image
	if cCfg.Image != "ghcr.io/astral-sh/uv:python3.13-bookworm-slim" {
		t.Errorf("expected image ghcr.io/astral-sh/uv:python3.13-bookworm-slim, got %s", cCfg.Image)
	}

	// Command
	if len(cCfg.Cmd) != len(cmd) || cCfg.Cmd[0] != cmd[0] {
		t.Errorf("expected cmd %v, got %v", cmd, cCfg.Cmd)
	}

	// Entrypoint should be cleared
	if len(cCfg.Entrypoint) != 0 {
		t.Errorf("expected Entrypoint to be cleared, got %v", cCfg.Entrypoint)
	}

	// WorkingDir
	if cCfg.WorkingDir != "/" {
		t.Errorf("expected WorkingDir /, got %s", cCfg.WorkingDir)
	}

	// HOME=/tmp should be in env
	foundHome := false
	for _, e := range cCfg.Env {
		if e == "HOME=/tmp" {
			foundHome = true
		}
	}
	if !foundHome {
		t.Error("expected HOME=/tmp in container env")
	}

	// User should be set (UID:GID)
	if cCfg.User == "" {
		t.Error("expected User to be set with UID:GID")
	}
	if !strings.Contains(cCfg.User, ":") {
		t.Errorf("expected User in UID:GID format, got %s", cCfg.User)
	}

	// Labels
	if cCfg.Labels["ai.forge.agent"] != "test-agent-123" {
		t.Errorf("expected agent label test-agent-123, got %s", cCfg.Labels["ai.forge.agent"])
	}
	if cCfg.Labels["ai.forge.guild"] != "test-guild" {
		t.Errorf("expected guild label test-guild, got %s", cCfg.Labels["ai.forge.guild"])
	}

	// Network: none
	if hCfg.NetworkMode != "none" {
		t.Errorf("expected NetworkMode none, got %s", hCfg.NetworkMode)
	}

	// CPU
	if hCfg.NanoCPUs != 2500000000 {
		t.Errorf("expected NanoCPUs 2500000000, got %d", hCfg.NanoCPUs)
	}

	// Binds: /data with SELinux :z suffix
	if len(hCfg.Binds) != 1 {
		t.Fatalf("expected 1 bind, got %d: %v", len(hCfg.Binds), hCfg.Binds)
	}
	if hCfg.Binds[0] != "/data:/data:rw,z" {
		t.Errorf("expected bind /data:/data:rw,z, got %s", hCfg.Binds[0])
	}
}

func TestBuildContainerConfig_HostNetwork(t *testing.T) {
	agentSpec := &protocol.AgentSpec{ID: "agent-host"}
	entry := &registry.AgentRegistryEntry{
		Network: []string{"host"},
	}

	_, hCfg := BuildContainerConfig(agentSpec, entry, "guild-1", "ubuntu:24.04", nil, nil)

	if hCfg.NetworkMode != "host" {
		t.Errorf("expected NetworkMode host, got %s", hCfg.NetworkMode)
	}
}

func TestBuildContainerConfig_BridgeNetwork(t *testing.T) {
	agentSpec := &protocol.AgentSpec{ID: "agent-bridge"}
	entry := &registry.AgentRegistryEntry{
		Network: []string{"api.openai.com:443", "pypi.org:443"},
	}

	_, hCfg := BuildContainerConfig(agentSpec, entry, "guild-1", "ubuntu:24.04", nil, nil)

	if hCfg.NetworkMode != "bridge" {
		t.Errorf("expected NetworkMode bridge, got %s", hCfg.NetworkMode)
	}
}

func TestBuildContainerConfig_EmptyNetwork(t *testing.T) {
	agentSpec := &protocol.AgentSpec{ID: "agent-empty"}
	entry := &registry.AgentRegistryEntry{
		Network: []string{},
	}

	_, hCfg := BuildContainerConfig(agentSpec, entry, "guild-1", "ubuntu:24.04", nil, nil)

	if hCfg.NetworkMode != "none" {
		t.Errorf("expected NetworkMode none for empty network, got %s", hCfg.NetworkMode)
	}
}

func TestBuildContainerConfig_ReadOnlyBind(t *testing.T) {
	agentSpec := &protocol.AgentSpec{ID: "agent-ro"}
	entry := &registry.AgentRegistryEntry{
		Filesystem: []registry.FilesystemPermission{
			{Path: "/config", Mode: "ro"},
		},
	}

	_, hCfg := BuildContainerConfig(agentSpec, entry, "guild-1", "ubuntu:24.04", nil, nil)

	if len(hCfg.Binds) != 1 {
		t.Fatalf("expected 1 bind, got %d", len(hCfg.Binds))
	}
	if hCfg.Binds[0] != "/config:/config:ro,z" {
		t.Errorf("expected bind /config:/config:ro,z, got %s", hCfg.Binds[0])
	}
}
