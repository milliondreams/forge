package registry

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"gopkg.in/yaml.v3"
)

type RuntimeType string

const (
	RuntimeUVX    RuntimeType = "uvx"
	RuntimeDocker RuntimeType = "docker"
	RuntimeBinary RuntimeType = "binary"
)

// FilesystemPermission specifies host-to-container bind mounts
type FilesystemPermission struct {
	Path string `yaml:"path"`
	Mode string `yaml:"mode"` // e.g., "ro" or "rw"
}

// AgentRegistryEntry represents a single verifiable agent template in the registry
type AgentRegistryEntry struct {
	ID               string                          `yaml:"id"`
	ClassName        string                          `yaml:"class_name"`
	Description      string                          `yaml:"description,omitempty"`
	Runtime          RuntimeType                     `yaml:"runtime"`
	Package          string                          `yaml:"package,omitempty"`           // For uvx base package
	WithDependencies []string                        `yaml:"with_dependencies,omitempty"` // For uvx additional dependencies (e.g., local paths)
	Image            string                          `yaml:"image,omitempty"`             // For docker
	Executable       string                          `yaml:"executable,omitempty"`        // For binary
	Args             []string                        `yaml:"args,omitempty"`              // Additional args
	Requirements     protocol.CredentialRequirements `yaml:"requirements,omitempty"`
	Network          []string                        `yaml:"network,omitempty"`    // Authorized egress hosts/networks
	Filesystem       []FilesystemPermission          `yaml:"filesystem,omitempty"` // Host bind mounts
}

// RegistryConfig maps the root yaml structure
type RegistryConfig struct {
	Entries []AgentRegistryEntry `yaml:"entries"`
}

// Registry manages the collection of known agent types
type Registry struct {
	entries map[string]AgentRegistryEntry
}

// Load reads and strictly parses an agent registry YAML file and validates
// credential declarations. When oauthMgr is non-nil, every OAuth provider and
// requested scope must also exist in the configured provider catalog.
func Load(path string, oauthMgr *oauth.Manager) (*Registry, error) {
	if path == "" {
		path = os.Getenv("FORGE_AGENT_REGISTRY")
		if path == "" {
			path = "conf/forge-agent-registry.yaml" // Default fallback
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry file %s: %w", path, err)
	}

	var config RegistryConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to parse registry YAML %s: %w", path, err)
	}

	r := &Registry{
		entries: make(map[string]AgentRegistryEntry),
	}

	for _, entry := range config.Entries {
		if entry.ClassName == "" {
			return nil, fmt.Errorf("invalid registry entry %s: class_name is required", entry.ID)
		}
		if entry.Runtime == "" {
			return nil, fmt.Errorf("invalid registry entry %s: runtime is required", entry.ID)
		}
		entry.Requirements.Normalize()
		if err := entry.Requirements.Validate(); err != nil {
			return nil, fmt.Errorf("invalid registry entry %s requirements: %w", entry.ID, err)
		}
		r.entries[entry.ClassName] = entry
	}

	if err := r.ValidateOAuth(oauthMgr); err != nil {
		return nil, err
	}
	return r, nil
}

// Lookup finds an agent registry entry by its fully qualified class name
func (r *Registry) Lookup(className string) (*AgentRegistryEntry, error) {
	entry, exists := r.entries[className]
	if !exists {
		return nil, fmt.Errorf("agent class not found in registry: %s", className)
	}
	return &entry, nil
}

// InjectFilesystem adds a host bind mount to the specified agent class dynamically
func (r *Registry) InjectFilesystem(className string, fs FilesystemPermission) error {
	entry, exists := r.entries[className]
	if !exists {
		return fmt.Errorf("class not found: %s", className)
	}
	entry.Filesystem = append(entry.Filesystem, fs)
	r.entries[className] = entry
	return nil
}

// InjectNetwork appends authorized egress hosts/networks to the specified agent class dynamically
func (r *Registry) InjectNetwork(className string, networks []string) error {
	entry, exists := r.entries[className]
	if !exists {
		return fmt.Errorf("class not found: %s", className)
	}
	entry.Network = append(entry.Network, networks...)
	r.entries[className] = entry
	return nil
}

// ClassNames returns all registered class names.
func (r *Registry) ClassNames() []string {
	classNames := make([]string, 0, len(r.entries))
	for className := range r.entries {
		classNames = append(classNames, className)
	}
	return classNames
}

// ValidateOAuth checks every registry OAuth declaration against the configured
// provider catalog. Invalid declarations fail the complete registry load.
func (r *Registry) ValidateOAuth(oauthMgr *oauth.Manager) error {
	if oauthMgr == nil {
		return nil
	}
	for className, entry := range r.entries {
		for _, need := range entry.Requirements.OAuth {
			if err := oauthMgr.ActivateProviderRequirements(need.Provider, need.Scopes); err != nil {
				return fmt.Errorf("registry agent %q (class %s) OAuth requirements: %w", entry.ID, className, err)
			}
		}
	}
	return nil
}

// ResolveCommand generates the OS exec slice strings required to launch the given agent entry.
//
// extraDeps holds additional Python packages requested by the agent's spec
// (AgentSpec.ForgeExtraDeps). They are installed only into this agent's uvx environment,
// unlike the guild-wide FORGE_EXTRA_DEPS environment variable, and use the same value
// format: each element may itself be a comma-separated list.
func ResolveCommand(entry *AgentRegistryEntry, extraDeps []string) []string {
	var cmd []string

	switch entry.Runtime {
	case RuntimeUVX:
		cmd = append(cmd, ResolveUVXCommand())
		if py := UVPython(); py != "" {
			cmd = append(cmd, "--python", py)
		}
		forgePkg := os.Getenv("FORGE_PYTHON_PKG")
		if forgePkg == "" {
			forgePkg = "rusticai-forge"
		}
		cmd = append(cmd, "--with", forgePkg)

		for _, dep := range entry.WithDependencies {
			cmd = append(cmd, "--with", dep)
		}
		if envDeps := os.Getenv("FORGE_EXTRA_DEPS"); envDeps != "" {
			cmd = appendWithDeps(cmd, envDeps)
		}
		for _, dep := range extraDeps {
			cmd = appendWithDeps(cmd, dep)
		}
		if entry.Package != "" {
			for _, pkg := range strings.Split(entry.Package, ",") {
				cmd = append(cmd, "--with", strings.TrimSpace(pkg))
			}
		}
		cmd = append(cmd, "python", "-m", "rustic_ai.forge.agent_runner")

	case RuntimeDocker:
		cmd = append(cmd, "docker", "run", "--rm", entry.Image)

	case RuntimeBinary:
		if entry.Executable != "" {
			cmd = append(cmd, entry.Executable)
		}
		if len(entry.Args) > 0 {
			cmd = append(cmd, entry.Args...)
		}

	default:
		cmd = append(cmd, ResolveUVXCommand())
		if py := UVPython(); py != "" {
			cmd = append(cmd, "--python", py)
		}
		cmd = append(cmd, "python", "-m", "rustic_ai.forge.agent_runner")
	}

	return cmd
}

// DependencyRequirements returns the Python packages represented by the
// existing uvx command contract without changing that launch contract.
func DependencyRequirements(entry *AgentRegistryEntry, extraDeps []string) []string {
	forgePkg := strings.TrimSpace(os.Getenv("FORGE_PYTHON_PKG"))
	if forgePkg == "" {
		forgePkg = "rusticai-forge"
	}
	requirements := []string{forgePkg, "rusticai-core"}
	requirements = append(requirements, entry.WithDependencies...)
	if envDeps := os.Getenv("FORGE_EXTRA_DEPS"); envDeps != "" {
		requirements = appendDependencyValues(requirements, envDeps)
	}
	for _, dependency := range extraDeps {
		requirements = appendDependencyValues(requirements, dependency)
	}
	for _, pkg := range strings.Split(entry.Package, ",") {
		if pkg = strings.TrimSpace(pkg); pkg != "" {
			requirements = append(requirements, pkg)
		}
	}
	return requirements
}

func appendDependencyValues(requirements []string, value string) []string {
	for _, dependency := range strings.Split(value, ",") {
		if dependency = strings.TrimSpace(dependency); dependency != "" {
			requirements = append(requirements, dependency)
		}
	}
	return requirements
}

// appendWithDeps appends a `--with <dep>` pair for every non-empty, whitespace-trimmed
// entry in a comma-separated dependency list.
func appendWithDeps(cmd []string, deps string) []string {
	for _, dep := range strings.Split(deps, ",") {
		if dep = strings.TrimSpace(dep); dep != "" {
			cmd = append(cmd, "--with", dep)
		}
	}
	return cmd
}

// UVPython returns the interpreter request used to pin uv/uvx when spawning
// Python agents (passed as `uvx --python <value> …`). The value is a uv Python
// request: a bare version like "3.13", a constraint like ">=3.13,<3.14", or an
// absolute interpreter path. It is sourced from FORGE_UV_PYTHON, which the
// server/client/CLI set from their --uv-python flags. Empty means "let uv pick"
// (uv's default discovery), preserving prior behaviour.
func UVPython() string {
	return strings.TrimSpace(os.Getenv("FORGE_UV_PYTHON"))
}
