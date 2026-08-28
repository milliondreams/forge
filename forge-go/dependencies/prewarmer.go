package dependencies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

const (
	ModeOff   = "off"
	ModeGuild = "guild"
)

// ValidateMode keeps dependency preparation opt-in for generic Forge clients.
func ValidateMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeOff, ModeGuild:
		return nil
	default:
		return fmt.Errorf("unsupported client dependency prewarm mode %q (expected off or guild)", mode)
	}
}

func Enabled(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), ModeGuild)
}

type Metadata struct {
	GuildID        string
	AgentID        string
	OrganizationID string
	RequestID      string
}

type Config struct {
	Context       context.Context
	Registry      *registry.Registry
	Publisher     *infraevents.Publisher
	NodeID        string
	Workers       int
	UVXPath       string
	Python        string
	UVVersion     string
	ForgeRevision string
	Run           func(context.Context, string, []string, []string) error
}

type preparation struct {
	done chan struct{}
	once sync.Once
	err  error
}

func (p *preparation) complete(err error) {
	p.once.Do(func() {
		p.err = err
		close(p.done)
	})
}

type work struct {
	key          string
	requirements []string
	args         []string
	metadata     Metadata
	preparation  *preparation
}

// PreparationError identifies dependency gating failures without changing
// supervisor error contracts.
type PreparationError struct {
	Err error
}

func (e *PreparationError) Error() string {
	return "dependency_preparation_failed: " + e.Err.Error()
}

func (e *PreparationError) Unwrap() error { return e.Err }

// Coordinator owns process-wide single-flight dependency preparation.
type Coordinator struct {
	ctx           context.Context
	registry      *registry.Registry
	publisher     *infraevents.Publisher
	nodeID        string
	uvxPath       string
	python        string
	uvVersion     string
	forgeRevision string
	run           func(context.Context, string, []string, []string) error
	queue         chan work

	mu       sync.Mutex
	inflight map[string]*preparation
	ready    map[string]struct{}
}

func NewCoordinator(cfg Config) (*Coordinator, error) {
	if cfg.Context == nil {
		return nil, errors.New("dependency prewarmer context is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("dependency prewarmer registry is required")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.UVXPath == "" {
		cfg.UVXPath = registry.ResolveUVXCommand()
	}
	if cfg.Python == "" {
		cfg.Python = registry.UVPython()
	}
	if cfg.UVVersion == "" {
		cfg.UVVersion = detectUVVersion(cfg.Context, cfg.UVXPath)
	}
	if cfg.ForgeRevision == "" {
		cfg.ForgeRevision = strings.TrimSpace(os.Getenv("FORGE_REVISION"))
	}
	if cfg.Run == nil {
		cfg.Run = runCommand
	}

	c := &Coordinator{
		ctx:           cfg.Context,
		registry:      cfg.Registry,
		publisher:     cfg.Publisher,
		nodeID:        cfg.NodeID,
		uvxPath:       cfg.UVXPath,
		python:        cfg.Python,
		uvVersion:     cfg.UVVersion,
		forgeRevision: cfg.ForgeRevision,
		run:           cfg.Run,
		queue:         make(chan work, cfg.Workers*8),
		inflight:      make(map[string]*preparation),
		ready:         make(map[string]struct{}),
	}
	for range cfg.Workers {
		go c.worker()
	}
	go c.cancelInflight()
	return c, nil
}

func (c *Coordinator) cancelInflight() {
	<-c.ctx.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, prep := range c.inflight {
		prep.complete(c.ctx.Err())
		delete(c.inflight, key)
	}
}

func detectUVVersion(ctx context.Context, uvxPath string) string {
	versionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, uvxPath, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func runCommand(ctx context.Context, executable string, args, env []string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := sanitizeOutput(string(output), env)
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

func sanitizeOutput(output string, env []string) string {
	output = strings.TrimSpace(output)
	if len(output) > 2048 {
		output = output[len(output)-2048:]
	}
	words := strings.Fields(output)
	for i, word := range words {
		if scheme := strings.Index(word, "://"); scheme >= 0 {
			rest := word[scheme+3:]
			if at := strings.Index(rest, "@"); at >= 0 {
				words[i] = word[:scheme+3] + "***@" + rest[at+1:]
			}
		}
	}
	output = strings.Join(words, " ")
	for _, assignment := range env {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || value == "" {
			continue
		}
		upperKey := strings.ToUpper(key)
		if strings.Contains(upperKey, "TOKEN") || strings.Contains(upperKey, "PASSWORD") || strings.Contains(upperKey, "SECRET") || strings.Contains(upperKey, "CREDENTIAL") {
			output = strings.ReplaceAll(output, value, "***")
		}
	}
	return output
}

// WarmSystem primes the shared download cache without delaying client readiness.
func (c *Coordinator) WarmSystem() {
	entry := &registry.AgentRegistryEntry{Runtime: registry.RuntimeUVX}
	requirements := registry.DependencyRequirements(entry, nil)
	args := c.argsForEntry(entry, nil)
	_, _ = c.schedule(Metadata{}, requirements, args)
}

// PrepareGuild schedules the manager first and all static agents behind it, then
// waits only for the manager's exact environment.
func (c *Coordinator) PrepareGuild(ctx context.Context, metadata Metadata, managerEntry *registry.AgentRegistryEntry, managerExtraDeps []string, spec *protocol.GuildSpec) error {
	managerPreparation, err := c.schedule(metadata, registry.DependencyRequirements(managerEntry, managerExtraDeps), c.argsForEntry(managerEntry, managerExtraDeps))
	if err != nil {
		return &PreparationError{Err: err}
	}
	for i := range spec.Agents {
		agentSpec := &spec.Agents[i]
		entry, lookupErr := c.registry.Lookup(agentSpec.ClassName)
		if lookupErr != nil {
			c.emit(metadataForAgent(metadata, agentSpec.ID), "dependency.prepare.failed", infraevents.SeverityError, "dependency preparation could not resolve agent class", map[string]any{"error": lookupErr.Error()})
			continue
		}
		if entry.Runtime != registry.RuntimeUVX {
			continue
		}
		agentMetadata := metadataForAgent(metadata, agentSpec.ID)
		if _, scheduleErr := c.schedule(agentMetadata, registry.DependencyRequirements(entry, agentSpec.ForgeExtraDeps), c.argsForEntry(entry, agentSpec.ForgeExtraDeps)); scheduleErr != nil {
			c.emit(agentMetadata, "dependency.prepare.failed", infraevents.SeverityError, "dependency preparation could not be scheduled", map[string]any{"error": scheduleErr.Error()})
		}
	}
	return wait(ctx, managerPreparation)
}

// PrepareAgent gates one UVX spawn on its exact environment.
func (c *Coordinator) PrepareAgent(ctx context.Context, metadata Metadata, entry *registry.AgentRegistryEntry, extraDeps []string) error {
	if entry.Runtime != registry.RuntimeUVX {
		return nil
	}
	prep, err := c.schedule(metadata, registry.DependencyRequirements(entry, extraDeps), c.argsForEntry(entry, extraDeps))
	if err != nil {
		return &PreparationError{Err: err}
	}
	return wait(ctx, prep)
}

func metadataForAgent(metadata Metadata, agentID string) Metadata {
	metadata.AgentID = agentID
	return metadata
}

func wait(ctx context.Context, prep *preparation) error {
	select {
	case <-ctx.Done():
		return &PreparationError{Err: ctx.Err()}
	case <-prep.done:
		if prep.err != nil {
			return &PreparationError{Err: prep.err}
		}
		return nil
	}
}

func (c *Coordinator) schedule(metadata Metadata, requirements, args []string) (*preparation, error) {
	normalized, err := normalizeRequirements(requirements)
	if err != nil {
		return nil, err
	}
	key := c.key(normalized)

	c.mu.Lock()
	if err := c.ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if _, ok := c.ready[key]; ok {
		c.mu.Unlock()
		prep := &preparation{done: make(chan struct{})}
		close(prep.done)
		c.emit(metadata, "dependency.prepare.completed", infraevents.SeverityInfo, "dependency environment already prepared", map[string]any{"key": key, "cache_state": "memory", "duration_ms": 0})
		return prep, nil
	}
	if prep := c.inflight[key]; prep != nil {
		c.mu.Unlock()
		return prep, nil
	}
	prep := &preparation{done: make(chan struct{})}
	c.inflight[key] = prep
	c.mu.Unlock()

	item := work{key: key, requirements: normalized, args: append([]string(nil), args...), metadata: metadata, preparation: prep}
	select {
	case c.queue <- item:
		return prep, nil
	case <-c.ctx.Done():
		c.mu.Lock()
		delete(c.inflight, key)
		prep.complete(c.ctx.Err())
		c.mu.Unlock()
		return nil, c.ctx.Err()
	}
}

func (c *Coordinator) argsForEntry(entry *registry.AgentRegistryEntry, extraDeps []string) []string {
	command := registry.ResolveCommand(entry, extraDeps)
	if len(command) < 4 {
		return nil
	}
	args := append([]string(nil), command[1:len(command)-3]...)
	return append(args, "python", "-c", "import rustic_ai.forge.agent_runner")
}

func normalizeRequirements(requirements []string) ([]string, error) {
	normalized := make([]string, 0, len(requirements))
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			continue
		}
		if strings.ContainsAny(requirement, "\r\n\x00") {
			return nil, fmt.Errorf("dependency requirement contains prohibited control characters")
		}
		if _, ok := seen[requirement]; ok {
			continue
		}
		seen[requirement] = struct{}{}
		normalized = append(normalized, requirement)
	}
	if len(normalized) == 0 {
		return nil, errors.New("dependency requirements are empty")
	}
	slices.Sort(normalized)
	return normalized, nil
}

func (c *Coordinator) key(requirements []string) string {
	indexFingerprint := sha256.Sum256([]byte(strings.Join([]string{
		os.Getenv("UV_INDEX"), os.Getenv("UV_INDEX_URL"), os.Getenv("UV_DEFAULT_INDEX"), os.Getenv("UV_EXTRA_INDEX_URL"),
	}, "\x00")))
	material := strings.Join([]string{
		runtime.GOOS, runtime.GOARCH, c.uvxPath, c.uvVersion, c.python,
		c.forgeRevision, os.Getenv("FORGE_PYTHON_PKG"), hex.EncodeToString(indexFingerprint[:]),
		strings.Join(requirements, "\x00"),
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (c *Coordinator) worker() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case item := <-c.queue:
			c.execute(item)
		}
	}
}

func (c *Coordinator) execute(item work) {
	start := time.Now()
	c.emit(item.metadata, "dependency.prepare.started", infraevents.SeverityInfo, "dependency preparation started", map[string]any{"key": item.key, "cache_state": "uv"})
	env := os.Environ()
	if os.Getenv("UV_CACHE_DIR") == "" {
		env = append(env, "UV_CACHE_DIR="+forgepath.Resolve("uv_cache"))
	}
	err := c.run(c.ctx, c.uvxPath, item.args, env)
	duration := time.Since(start)

	c.mu.Lock()
	delete(c.inflight, item.key)
	if err == nil && c.ctx.Err() == nil {
		c.ready[item.key] = struct{}{}
	}
	item.preparation.complete(err)
	c.mu.Unlock()

	detail := map[string]any{"key": item.key, "cache_state": "uv", "duration_ms": duration.Milliseconds()}
	if err != nil {
		detail["error"] = err.Error()
		c.emit(item.metadata, "dependency.prepare.failed", infraevents.SeverityError, "dependency preparation failed", detail)
		return
	}
	c.emit(item.metadata, "dependency.prepare.completed", infraevents.SeverityInfo, "dependency preparation completed", detail)
}

func (c *Coordinator) emit(metadata Metadata, kind, severity, message string, detail map[string]any) {
	_ = c.publisher.Emit(c.ctx, infraevents.EmitParams{
		Kind: kind, Severity: severity, GuildID: metadata.GuildID, AgentID: metadata.AgentID,
		OrganizationID: metadata.OrganizationID, RequestID: metadata.RequestID, NodeID: c.nodeID,
		SourceComponent: "forge-go.dependency-prewarmer", SourceInstanceID: c.nodeID,
		Message: message, Detail: detail,
	})
}
