package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateAgentOSConfigRequiresFailClosedDependencies(t *testing.T) {
	dir := t.TempDir()
	bwrap := filepath.Join(dir, "bwrap")
	if err := os.WriteFile(bwrap, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, name := range []string{
		"FORGE_AGENT_REGISTRY",
		"FORGE_LOCAL_MODEL_CATALOG",
		"FORGE_OAUTH_PROVIDERS_CONFIG",
	} {
		t.Setenv(name, filepath.Join(dir, name))
	}
	for _, name := range []string{
		"FORGE_OAUTH_TOKEN_STORE",
		"FORGE_OAUTH_CLIENT_STORE",
		"FORGE_SECRET_STORE",
	} {
		t.Setenv(name, "keychain")
	}
	t.Setenv("AGENTOS_LOCAL_MODEL_BASE_URL", "http://10.0.2.101:55262/v1")
	t.Setenv("FORGE_UV_PYTHON", "3.13")
	t.Setenv("UV_NO_BUILD", "true")
	lock := []byte("rusticai-core==1 --hash=sha256:" + strings.Repeat("a", 64))
	lockPath := filepath.Join(dir, "python-requirements.lock")
	if err := os.WriteFile(lockPath, lock, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(lock)
	uvPath := filepath.Join(dir, "uv")
	pythonPath := filepath.Join(dir, "python3.13")
	for _, path := range []string{uvPath, pythonPath} {
		if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dependencyRoot := filepath.Join(dir, "dependency-state")
	if err := os.Mkdir(dependencyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_AGENTOS_UV_PATH", uvPath)
	t.Setenv("FORGE_AGENTOS_PYTHON_PATH", pythonPath)
	t.Setenv("FORGE_AGENTOS_SYSTEM_LOCK", lockPath)
	t.Setenv("FORGE_AGENTOS_SYSTEM_LOCK_SHA256", hex.EncodeToString(sum[:]))
	t.Setenv("FORGE_AGENTOS_DEPENDENCY_ROOT", dependencyRoot)
	t.Setenv("FORGE_AGENTOS_DEPENDENCY_INDEX", "https://pypi.org/simple")
	t.Setenv("FORGE_AGENTOS_DEPENDENCY_DOMAINS", "files.pythonhosted.org,pypi.org")
	cfg := &ServerConfig{
		AgentOSMode:             true,
		AgentOSStateSchema:      1,
		WithClient:              true,
		ClientDefaultSupervisor: "bwrap",
		ClientDefaultTransport:  "supervisor-zmq",
		ClientZMQBridgeMode:     "ipc",
		DataDir:                 filepath.Join(dir, "data"),
		DependencyConfig:        filepath.Join(dir, "dependencies.yaml"),
		ShutdownTimeout:         time.Second,
	}
	if err := validateAgentOSConfig(cfg); err != nil {
		t.Fatalf("validateAgentOSConfig: %v", err)
	}
	prerequisites, err := inspectAgentOSConfig(cfg)
	if err != nil {
		t.Fatalf("inspectAgentOSConfig: %v", err)
	}
	if len(prerequisites) == 0 {
		t.Fatal("expected machine-readable prerequisites")
	}
	for _, prerequisite := range prerequisites {
		if !prerequisite.Satisfied {
			t.Fatalf("prerequisite %s was not satisfied: %s", prerequisite.Name, prerequisite.Detail)
		}
	}
	cfg.ClientDefaultSupervisor = "process"
	if err := validateAgentOSConfig(cfg); err == nil {
		t.Fatal("expected unrestricted process supervisor to fail")
	}
}

func TestEnableAgentOSFromEnvironment(t *testing.T) {
	t.Setenv("AGENTOS_MODE", "true")
	if !enableAgentOSFromEnvironment(false) {
		t.Fatal("expected AGENTOS_MODE to enable AgentOS policy")
	}
}

func TestEmbeddedClientServerURLUsesAgentOSLoopbackOverride(t *testing.T) {
	cfg := &ServerConfig{
		ListenAddress:     "0.0.0.0:3001",
		ManagerAPIBaseURL: "http://127.0.0.1:3001",
		AgentOSMode:       true,
	}
	if got := embeddedClientServerURL(cfg); got != cfg.ManagerAPIBaseURL {
		t.Fatalf("AgentOS embedded client URL = %q, want %q", got, cfg.ManagerAPIBaseURL)
	}
}

func TestEmbeddedClientServerURLPreservesNativeDerivation(t *testing.T) {
	cfg := &ServerConfig{
		ListenAddress:     "0.0.0.0:3001",
		ManagerAPIBaseURL: "http://127.0.0.1:3001",
	}
	if got := embeddedClientServerURL(cfg); got != "http://0.0.0.0:3001" {
		t.Fatalf("native embedded client URL = %q, want existing listen-derived URL", got)
	}
}
