package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rustic-ai/forge/forge-go/api"
	"github.com/rustic-ai/forge/forge-go/credentials"
	"github.com/rustic-ai/forge/forge-go/localmodel"
)

func validateAgentOSConfig(cfg *ServerConfig) error {
	_, err := inspectAgentOSConfig(cfg)
	return err
}

func inspectAgentOSConfig(cfg *ServerConfig) ([]api.AgentOSPrerequisite, error) {
	if cfg == nil {
		return nil, fmt.Errorf("server config is required")
	}
	if !cfg.AgentOSMode {
		return nil, nil
	}

	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 20 * time.Second
	}

	prerequisites := make([]api.AgentOSPrerequisite, 0, 14)
	var failures []string
	add := func(name string, required, satisfied bool, detail string) {
		prerequisites = append(prerequisites, api.AgentOSPrerequisite{
			Name:      name,
			Required:  required,
			Satisfied: satisfied,
			Detail:    detail,
		})
		if required && !satisfied {
			failures = append(failures, name+": "+detail)
		}
	}

	add("embedded_client", true, cfg.WithClient, "requires --with-client")
	add("supervisor", true, cfg.ClientDefaultSupervisor == "bwrap", "requires --client-default-supervisor=bwrap")
	add("transport", true, cfg.ClientDefaultTransport == "supervisor-zmq", "requires --client-default-agent-transport=supervisor-zmq")
	bridgeValid := cfg.ClientZMQBridgeMode == "" || cfg.ClientZMQBridgeMode == "ipc"
	add("messaging_bridge", true, bridgeValid, "requires the IPC messaging bridge")

	_, bwrapErr := exec.LookPath("bwrap")
	bwrapDetail := "available"
	if bwrapErr != nil {
		bwrapDetail = bwrapErr.Error()
	}
	add("bubblewrap", true, bwrapErr == nil, bwrapDetail)

	for _, item := range []struct {
		name string
		path string
	}{
		{name: "data_dir", path: cfg.DataDir},
		{name: "dependency_config", path: cfg.DependencyConfig},
	} {
		add(item.name, true, filepath.IsAbs(item.path), "must be an absolute path")
	}

	for _, item := range []struct {
		name string
		env  string
	}{
		{name: "agent_registry", env: "FORGE_AGENT_REGISTRY"},
		{name: "local_model_catalog", env: "FORGE_LOCAL_MODEL_CATALOG"},
		{name: "oauth_providers_config", env: "FORGE_OAUTH_PROVIDERS_CONFIG"},
	} {
		path := strings.TrimSpace(os.Getenv(item.env))
		add(item.name, true, filepath.IsAbs(path), item.env+" must be an absolute path")
	}

	credentialDir := strings.TrimSpace(os.Getenv(credentials.AgentOSDirectoryEnv))
	credentialInfo, credentialErr := os.Stat(credentialDir)
	credentialValid := filepath.IsAbs(credentialDir) && credentialErr == nil && credentialInfo.IsDir() && credentialInfo.Mode().Perm() == 0o700
	add("credential_storage", true, credentialValid, credentials.AgentOSDirectoryEnv+" must name an existing mode-0700 absolute directory")
	add("credential_backend", true, true, credentials.BackendName)

	add("state_schema", true, cfg.AgentOSStateSchema > 0, "must be positive")
	add("python_version", true, strings.TrimSpace(os.Getenv("FORGE_UV_PYTHON")) == "3.13", "FORGE_UV_PYTHON must equal 3.13")
	add("python_no_build", true, strings.EqualFold(strings.TrimSpace(os.Getenv("UV_NO_BUILD")), "true"), "UV_NO_BUILD must equal true")
	for _, item := range []struct {
		name string
		env  string
	}{
		{name: "dependency_uv", env: "FORGE_AGENTOS_UV_PATH"},
		{name: "dependency_python", env: "FORGE_AGENTOS_PYTHON_PATH"},
		{name: "dependency_lock", env: "FORGE_AGENTOS_SYSTEM_LOCK"},
	} {
		path := strings.TrimSpace(os.Getenv(item.env))
		info, statErr := os.Stat(path)
		add(item.name, true, filepath.IsAbs(path) && statErr == nil && !info.IsDir(), item.env+" must name an existing absolute file")
	}
	dependencyRoot := strings.TrimSpace(os.Getenv("FORGE_AGENTOS_DEPENDENCY_ROOT"))
	rootInfo, rootErr := os.Stat(dependencyRoot)
	add("dependency_storage", true, filepath.IsAbs(dependencyRoot) && rootErr == nil && rootInfo.IsDir() && rootInfo.Mode().Perm()&0o200 != 0, "FORGE_AGENTOS_DEPENDENCY_ROOT must be an existing writable absolute directory")
	lockDigest := strings.TrimSpace(os.Getenv("FORGE_AGENTOS_SYSTEM_LOCK_SHA256"))
	add("dependency_lock_digest", true, len(lockDigest) == 64 && strings.Trim(lockDigest, "0123456789abcdef") == "", "FORGE_AGENTOS_SYSTEM_LOCK_SHA256 must be a lowercase SHA-256 digest")
	add("dependency_index", true, strings.TrimSpace(os.Getenv("FORGE_AGENTOS_DEPENDENCY_INDEX")) == "https://pypi.org/simple", "only the signed public PyPI MVP index is supported")
	add("dependency_domains", true, strings.TrimSpace(os.Getenv("FORGE_AGENTOS_DEPENDENCY_DOMAINS")) == "files.pythonhosted.org,pypi.org" || strings.TrimSpace(os.Getenv("FORGE_AGENTOS_DEPENDENCY_DOMAINS")) == "pypi.org,files.pythonhosted.org", "signed PyPI infrastructure domains are required")

	endpoint, configured, endpointErr := localmodel.EndpointOverride()
	endpointDetail := endpoint
	if endpointErr != nil {
		endpointDetail = endpointErr.Error()
	} else if !configured {
		endpointDetail = localmodel.BaseURLEnv + " is not configured"
	}
	add("local_model_endpoint", configured, configured && endpointErr == nil, endpointDetail)

	if len(failures) > 0 {
		return prerequisites, fmt.Errorf("AgentOS prerequisites not satisfied: %s", strings.Join(failures, "; "))
	}
	return prerequisites, nil
}

func enableAgentOSFromEnvironment(configured bool) bool {
	if configured {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTOS_MODE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
