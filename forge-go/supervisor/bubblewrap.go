//go:build !windows

package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

type BubblewrapSupervisor struct {
	mu                 sync.RWMutex
	agents             map[string]*ManagedAgent
	bridges            map[string]*AgentMessagingBridge
	networkRelays      map[string]*AgentNetworkRelay
	systemTCPRelays    map[string]*AgentTCPRelay
	statusStore        AgentStatusStore
	msgBackend         messaging.Backend
	defaultTransport   protocol.AgentTransportMode
	zmqBridgeMode      BridgeTransportMode
	agentOSMode        bool
	materializer       *DependencyMaterializer
	managerAPIBaseURL  string
	systemRedisAddress string
}

// BubblewrapSupervisorOption configures a BubblewrapSupervisor.
type BubblewrapSupervisorOption func(*BubblewrapSupervisor)

// WithBubblewrapDefaultTransport sets the default agent transport mode.
func WithBubblewrapDefaultTransport(mode string) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.defaultTransport = protocol.NormalizeAgentTransportMode(mode)
	}
}

// WithBubblewrapMessagingBackend sets the messaging backend used for ZMQ bridges.
func WithBubblewrapMessagingBackend(backend messaging.Backend) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.msgBackend = backend
	}
}

// WithBubblewrapZMQBridgeMode sets whether ZMQ bridges use IPC or TCP.
func WithBubblewrapZMQBridgeMode(mode BridgeTransportMode) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.zmqBridgeMode = mode
	}
}

// WithBubblewrapAgentOSMode enables the fail-closed guest policy: no inherited
// host environment, no guest control-plane state, and no shared network
// namespace.
func WithBubblewrapAgentOSMode(enabled bool) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.agentOSMode = enabled
	}
}

// WithBubblewrapDependencyMaterializer installs the trusted AgentOS-only
// materializer. It is deliberately ignored by native launch construction.
func WithBubblewrapDependencyMaterializer(materializer *DependencyMaterializer) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.materializer = materializer
	}
}

// WithBubblewrapManagerAPIBaseURL configures the sole loopback control-plane
// destination available to AgentOS system agents through their private relay.
func WithBubblewrapManagerAPIBaseURL(baseURL string) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.managerAPIBaseURL = strings.TrimSpace(baseURL)
	}
}

// WithBubblewrapSystemRedisAddress configures the loopback Redis endpoint
// exposed to AgentOS system agents over a private Unix-socket relay.
func WithBubblewrapSystemRedisAddress(address string) BubblewrapSupervisorOption {
	return func(b *BubblewrapSupervisor) {
		b.systemRedisAddress = strings.TrimSpace(address)
	}
}

func NewBubblewrapSupervisor(statusStore AgentStatusStore, opts ...BubblewrapSupervisorOption) *BubblewrapSupervisor {
	b := &BubblewrapSupervisor{
		agents:           make(map[string]*ManagedAgent),
		bridges:          make(map[string]*AgentMessagingBridge),
		networkRelays:    make(map[string]*AgentNetworkRelay),
		systemTCPRelays:  make(map[string]*AgentTCPRelay),
		statusStore:      statusStore,
		defaultTransport: protocol.AgentTransportDirect,
		zmqBridgeMode:    BridgeTransportIPC,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b
}

func (p *BubblewrapSupervisor) agentOSRelayDestinations(entry *registry.AgentRegistryEntry, agentSpec *protocol.AgentSpec) ([]string, error) {
	if !p.agentOSMode {
		return nil, nil
	}
	if registry.IsSystemAgentClass(agentSpec.ClassName) {
		destination, err := agentOSManagerDestination(p.managerAPIBaseURL)
		if err != nil {
			return nil, fmt.Errorf("configure AgentOS system-agent control relay: %w", err)
		}
		return []string{destination}, nil
	}
	if len(entry.Network) == 0 || containsString(entry.Network, "none") {
		return nil, nil
	}
	return append([]string(nil), entry.Network...), nil
}

func (p *BubblewrapSupervisor) agentOSSystemRedisEndpoints(agentSpec *protocol.AgentSpec) (target, listen string, err error) {
	if !p.agentOSMode || !registry.IsSystemAgentClass(agentSpec.ClassName) || p.systemRedisAddress == "" {
		return "", "", nil
	}
	return agentOSRedisRelayEndpoints(p.systemRedisAddress)
}

func (p *BubblewrapSupervisor) Available() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

func (p *BubblewrapSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	guildID = normalizeGuildID(guildID)
	key := scopedAgentKey(guildID, agentSpec.ID)
	p.mu.Lock()
	if existing, exists := p.agents[key]; exists {
		state := existing.GetState()
		if state != StateStopped && state != StateFailed {
			p.mu.Unlock()
			return fmt.Errorf("agent %s is already managed in guild %s", agentSpec.ID, normalizeGuildID(guildID))
		}
		delete(p.agents, key)
	}

	agent := NewManagedAgent(guildID, agentSpec.ID)
	p.agents[key] = agent
	p.mu.Unlock()

	entry, err := reg.Lookup(agentSpec.ClassName)
	if err != nil {
		agent.SetState(StateFailed)
		return fmt.Errorf("failed to lookup agent class %s: %w", agentSpec.ClassName, err)
	}
	if p.agentOSMode {
		for _, filesystem := range entry.Filesystem {
			clean := filepath.Clean(filesystem.Path)
			if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") {
				agent.SetState(StateFailed)
				return fmt.Errorf("AgentOS filesystem permission %q is outside /workspace", filesystem.Path)
			}
		}
	}
	var dependencyEnvironment *DependencyEnvironment
	runtimeCmd := registry.ResolveCommand(entry, agentSpec.ForgeExtraDeps)
	if p.agentOSMode && entry.Runtime == registry.RuntimeUVX {
		if p.materializer == nil {
			agent.SetState(StateFailed)
			return errors.New("dependency_materialization_failed: AgentOS dependency materializer is unavailable")
		}
		dependencyEnvironment, err = p.materializer.Prepare(ctx, DependencyRequest{
			Requirements: registry.DependencyRequirements(entry, agentSpec.ForgeExtraDeps),
		})
		if err != nil {
			agent.SetState(StateFailed)
			agent.LastError = err
			return err
		}
		agent.SetDependencyEnvironment(dependencyEnvironment)
		runtimeCmd = []string{dependencyEnvironmentTarget + "/bin/python", "-m", "rustic_ai.forge.agent_runner"}
	}
	if len(runtimeCmd) == 0 {
		agent.ReleaseDependencyEnvironment()
		return fmt.Errorf("runtimeCmd is empty")
	}

	var networkRelay *AgentNetworkRelay
	relayDestinations, err := p.agentOSRelayDestinations(entry, agentSpec)
	if err != nil {
		agent.SetState(StateFailed)
		return err
	}
	if len(relayDestinations) > 0 {
		networkRelay, err = NewAgentNetworkRelay(agentNetworkRelayRoot, key, relayDestinations)
		if err != nil {
			agent.SetState(StateFailed)
			return fmt.Errorf("create scoped agent network relay: %w", err)
		}
		runtimeCmd = append([]string{
			"/opt/agentos/bin/agentos-netproxy",
			"--socket", networkRelay.SocketPath(),
			"--listen", "127.0.0.1:18080",
			"--",
		}, runtimeCmd...)
		env = append(env,
			"HTTP_PROXY=http://127.0.0.1:18080",
			"HTTPS_PROXY=http://127.0.0.1:18080",
			"http_proxy=http://127.0.0.1:18080",
			"https_proxy=http://127.0.0.1:18080",
			"NO_PROXY=",
			"no_proxy=",
		)
	}

	var systemTCPRelay *AgentTCPRelay
	redisTarget, redisListen, err := p.agentOSSystemRedisEndpoints(agentSpec)
	if err != nil {
		if networkRelay != nil {
			_ = networkRelay.Close()
		}
		agent.SetState(StateFailed)
		return fmt.Errorf("configure AgentOS system-agent Redis relay: %w", err)
	}
	if redisTarget != "" {
		systemTCPRelay, err = NewAgentTCPRelay(agentNetworkRelayRoot, key, redisTarget)
		if err != nil {
			if networkRelay != nil {
				_ = networkRelay.Close()
			}
			agent.SetState(StateFailed)
			return fmt.Errorf("create scoped AgentOS system-agent Redis relay: %w", err)
		}
		runtimeCmd = append([]string{
			"/opt/agentos/bin/agentos-netproxy",
			"--socket", systemTCPRelay.SocketPath(),
			"--listen", redisListen,
			"--",
		}, runtimeCmd...)
	}

	// Create ZMQ bridge when transport requires it.
	var bridge *AgentMessagingBridge
	transport := transportFromEnv(env, p.defaultTransport)
	if transport == protocol.AgentTransportSupervisorZMQ {
		if p.msgBackend == nil {
			if networkRelay != nil {
				_ = networkRelay.Close()
			}
			if systemTCPRelay != nil {
				_ = systemTCPRelay.Close()
			}
			agent.SetState(StateFailed)
			return fmt.Errorf("supervisor-zmq transport requires a messaging backend")
		}

		bridge, err = NewAgentMessagingBridgeWithMode(ctx, guildID, agentSpec.ID, "", p.msgBackend, p.zmqBridgeMode)
		if err != nil {
			if networkRelay != nil {
				_ = networkRelay.Close()
			}
			if systemTCPRelay != nil {
				_ = systemTCPRelay.Close()
			}
			agent.SetState(StateFailed)
			return fmt.Errorf("failed to create agent messaging bridge: %w", err)
		}

		env, err = applySupervisorTransportEnv(env, bridge)
		if err != nil {
			bridge.Close()
			if networkRelay != nil {
				_ = networkRelay.Close()
			}
			if systemTCPRelay != nil {
				_ = systemTCPRelay.Close()
			}
			agent.SetState(StateFailed)
			return fmt.Errorf("failed to configure supervisor transport env: %w", err)
		}
	}

	bwrapArgs := p.buildBwrapArgsWithRelays(entry, runtimeCmd, bridge, networkRelay, systemTCPRelay, dependencyEnvironment, env)

	if err := p.startProcess(ctx, guildID, agent, agentSpec, bwrapArgs, env); err != nil {
		if bridge != nil {
			bridge.Close()
		}
		if networkRelay != nil {
			_ = networkRelay.Close()
		}
		if systemTCPRelay != nil {
			_ = systemTCPRelay.Close()
		}
		agent.ReleaseDependencyEnvironment()
		return err
	}

	if bridge != nil {
		p.setBridge(guildID, agentSpec.ID, bridge)
	}
	if networkRelay != nil {
		p.setNetworkRelay(guildID, agentSpec.ID, networkRelay)
	}
	if systemTCPRelay != nil {
		p.setSystemTCPRelay(guildID, agentSpec.ID, systemTCPRelay)
	}

	return nil
}

func (p *BubblewrapSupervisor) buildBwrapArgs(entry *registry.AgentRegistryEntry, cmd []string, bridge *AgentMessagingBridge, env []string) []string {
	return p.buildBwrapArgsWithRelay(entry, cmd, bridge, nil, env)
}

func (p *BubblewrapSupervisor) buildBwrapArgsWithRelay(entry *registry.AgentRegistryEntry, cmd []string, bridge *AgentMessagingBridge, relay *AgentNetworkRelay, env []string) []string {
	return p.buildBwrapArgsWithEnvironment(entry, cmd, bridge, relay, nil, env)
}

func (p *BubblewrapSupervisor) buildBwrapArgsWithEnvironment(entry *registry.AgentRegistryEntry, cmd []string, bridge *AgentMessagingBridge, relay *AgentNetworkRelay, dependencyEnvironment *DependencyEnvironment, env []string) []string {
	return p.buildBwrapArgsWithRelays(entry, cmd, bridge, relay, nil, dependencyEnvironment, env)
}

func (p *BubblewrapSupervisor) buildBwrapArgsWithRelays(entry *registry.AgentRegistryEntry, cmd []string, bridge *AgentMessagingBridge, relay *AgentNetworkRelay, systemTCPRelay *AgentTCPRelay, dependencyEnvironment *DependencyEnvironment, env []string) []string {
	var args []string

	args = append(args,
		"--unshare-all",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--die-with-parent",
	)
	if p.agentOSMode {
		if dependencyEnvironment != nil {
			args = append(args, "--ro-bind", dependencyEnvironment.Path, dependencyEnvironmentTarget)
		}
		args = append(args,
			"--tmpfs", "/var/lib/agentos",
			"--tmpfs", "/run",
			"--tmpfs", "/home",
			"--tmpfs", "/workspace",
			"--dir", "/home/agent",
			"--new-session",
		)
	}

	needsNetwork := len(entry.Network) > 0 && !containsString(entry.Network, "none")

	// TCP bridge requires loopback access.
	if bridge != nil && bridge.Mode() == BridgeTransportTCP {
		needsNetwork = true
	}

	if needsNetwork && !p.agentOSMode {
		args = append(args, "--share-net")
	}

	for _, fs := range entry.Filesystem {
		if fs.Mode == "rw" {
			args = append(args, "--bind", fs.Path, fs.Path)
		} else {
			args = append(args, "--ro-bind", fs.Path, fs.Path)
		}
	}

	// IPC bridge: bind-mount the socket directory into the sandbox.
	// This must come after --tmpfs /tmp so it overlays correctly.
	if bridge != nil && bridge.Mode() == BridgeTransportIPC {
		socketDir := filepath.Dir(bridge.SocketPath())
		args = append(args, "--bind", socketDir, socketDir)
	}
	if relay != nil {
		socketDir := filepath.Dir(relay.SocketPath())
		args = append(args, "--ro-bind", socketDir, socketDir)
	}
	if systemTCPRelay != nil {
		socketDir := filepath.Dir(systemTCPRelay.SocketPath())
		if relay == nil || socketDir != filepath.Dir(relay.SocketPath()) {
			args = append(args, "--ro-bind", socketDir, socketDir)
		}
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" && !p.agentOSMode {
		for _, path := range bubblewrapWritablePaths(homeDir, env) {
			if err := os.MkdirAll(path, 0755); err != nil {
				slog.Warn("failed to create host path for bubblewrap bind", "path", path, "err", err)
				continue
			}
			args = append(args, "--bind", path, path)
		}
	}

	args = append(args, "--")
	args = append(args, cmd...)

	return args
}

func (p *BubblewrapSupervisor) startProcess(ctx context.Context, guildID string, agent *ManagedAgent, agentSpec *protocol.AgentSpec, bwrapArgs []string, env []string) error {
	guildID = normalizeGuildID(guildID)
	ctx, span := otel.Tracer("forge.supervisor").Start(ctx, "supervisor.bwrap.spawn")
	defer span.End()

	cmd := exec.CommandContext(ctx, "bwrap", bwrapArgs...)
	cmd.Env = bubblewrapChildEnv(env, p.agentOSMode)

	propagator := otel.GetTextMapPropagator()
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	if tp, ok := carrier["traceparent"]; ok {
		cmd.Env = append(cmd.Env, fmt.Sprintf("TRACEPARENT=%s", tp))
	}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	startBootTime := time.Now()
	if err := cmd.Start(); err != nil {
		agent.SetState(StateFailed)
		agent.LastError = err
		return fmt.Errorf("failed to start agent process %s: %w", agent.ID, err)
	}

	telemetry.ObserveSupervisorBootDuration("local-node", "bwrap", time.Since(startBootTime))

	agent.SetPID(cmd.Process.Pid)
	agent.SetState(StateRunning)

	logger := slog.With("agent_id", agent.ID, "guild_id", guildID, "node_id", "local-node", "supervisor", "bwrap")

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			logger.Info(scanner.Text(), "source", "agent_stdout")
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			logger.Error(scanner.Text(), "source", "agent_stderr")
		}
	}()

	_ = applyResourceLimits(cmd.Process.Pid, agentSpec)

	if p.statusStore != nil {
		_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "running", NodeID: "local-node", PID: cmd.Process.Pid, Timestamp: time.Now()}, 30*time.Second)
	}

	go p.monitorProcess(guildID, agent, agentSpec, cmd, bwrapArgs, env)

	return nil
}

func bubblewrapChildEnv(env []string, agentOSMode bool) []string {
	if !agentOSMode {
		return append(os.Environ(), env...)
	}
	denied := map[string]struct{}{
		"DBUS_SESSION_BUS_ADDRESS": {},
		"GNOME_KEYRING_CONTROL":    {},
		"FORGE_DATABASE_URL":       {},
		"FORGE_SECRET_STORE":       {},
		"FORGE_OAUTH_TOKEN_STORE":  {},
		"FORGE_OAUTH_CLIENT_STORE": {},
		"REDIS_HOST":               {},
		"REDIS_PORT":               {},
		"NATS_URL":                 {},
		"LD_PRELOAD":               {},
		"LD_LIBRARY_PATH":          {},
		"PYTHONPATH":               {},
		"PYTHONHOME":               {},
		"DBUS_SYSTEM_BUS_ADDRESS":  {},
		"PATH":                     {},
		"HOME":                     {},
		"TMPDIR":                   {},
		"LANG":                     {},
	}
	out := []string{
		"PATH=/opt/agentos/python/bin:/usr/local/bin:/usr/bin:/bin",
		"HOME=/home/agent",
		"TMPDIR=/tmp",
		"LANG=C.UTF-8",
	}
	for _, value := range env {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if _, blocked := denied[key]; blocked {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (p *BubblewrapSupervisor) monitorProcess(guildID string, agent *ManagedAgent, agentSpec *protocol.AgentSpec, cmd *exec.Cmd, bwrapArgs []string, env []string) {
	guildID = normalizeGuildID(guildID)
	ctx, lifecycleSpan := otel.Tracer("forge.supervisor").Start(context.Background(), "bwrap.lifecycle")
	defer lifecycleSpan.End()

	ticker := time.NewTicker(10 * time.Second)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				if agent.GetState() == StateRunning {
					if p.statusStore != nil {
						_ = p.statusStore.RefreshStatus(ctx, guildID, agent.ID, 30*time.Second)
					}
					pid := agent.GetPID()
					if pid > 0 {
						if proc, err := process.NewProcess(int32(pid)); err == nil {
							if cpuPct, err := proc.CPUPercent(); err == nil {
								telemetry.SetAgentCPUCores(guildID, agent.ID, "local-node-bwrap", cpuPct)
							}
							if memInfo, err := proc.MemoryInfo(); err == nil {
								telemetry.SetAgentMemoryBytes(guildID, agent.ID, "local-node-bwrap", float64(memInfo.RSS))
							}
						}
					}
				}
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	p.stopBridge(guildID, agent.ID)

	agent.LastExitAt = time.Now()
	exitCode := "1"
	if err == nil {
		exitCode = "0"
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = fmt.Sprintf("%d", exitErr.ExitCode())
	}

	telemetry.AddAgentExitCode(guildID, agent.ID, "local-node-bwrap", exitCode)

	if agent.IsStopRequested() {
		p.stopNetworkRelay(guildID, agent.ID)
		agent.ReleaseDependencyEnvironment()
		agent.SetState(StateStopped)
		if p.statusStore != nil {
			_ = p.statusStore.DeleteStatus(ctx, guildID, agent.ID)
		}
		return
	}

	agent.SetState(StateRestarting)
	agent.LastError = err

	if p.statusStore != nil {
		_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "restarting", Timestamp: time.Now()}, 30*time.Second)
	}

	if time.Since(agent.StartedAt) > StableTime {
		agent.RestartCount = 0
	}

	agent.RestartCount++

	delay := ComputeBackoff(agent.RestartCount)
	if delay == 0 {
		p.stopNetworkRelay(guildID, agent.ID)
		agent.ReleaseDependencyEnvironment()
		agent.SetState(StateFailed)
		if p.statusStore != nil {
			_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "failed", Timestamp: time.Now()}, 300*time.Second)
		}
		return
	}

	slog.Info("agent crashed, restarting", "agent_id", agent.ID, "delay", delay, "attempt", agent.RestartCount)

	select {
	case <-time.After(delay):
		if !agent.IsStopRequested() {
			if err := p.startProcess(ctx, guildID, agent, agentSpec, bwrapArgs, env); err != nil {
				slog.Error("failed to restart bwrap-managed agent", "guild_id", guildID, "agent_id", agent.ID, "error", err)
				agent.SetState(StateFailed)
				agent.LastError = err
				if p.statusStore != nil {
					_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "failed", Timestamp: time.Now()}, 300*time.Second)
				}
				agent.ReleaseDependencyEnvironment()
			}
		}
	case <-agent.stopCh:
		p.stopNetworkRelay(guildID, agent.ID)
		agent.ReleaseDependencyEnvironment()
		agent.SetState(StateStopped)
		if p.statusStore != nil {
			_ = p.statusStore.DeleteStatus(ctx, guildID, agent.ID)
		}
	}
}

func (p *BubblewrapSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	key := scopedAgentKey(guildID, agentID)
	p.mu.RLock()
	agent, exists := p.agents[key]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not managed in guild %s", agentID, normalizeGuildID(guildID))
	}

	agent.RequestStop()
	pid := agent.GetPID()

	if pid > 0 {
		pgid, err := syscall.Getpgid(pid)
		if err == nil {
			if killErr := syscall.Kill(-pgid, syscall.SIGTERM); killErr != nil && killErr != syscall.ESRCH {
				slog.Warn("failed to SIGTERM process group", "pid", pid, "pgid", pgid, "error", killErr)
			}
		} else {
			if killErr := syscall.Kill(pid, syscall.SIGTERM); killErr != nil && killErr != syscall.ESRCH {
				slog.Warn("failed to SIGTERM process", "pid", pid, "error", killErr)
			}
		}

		deadline := time.NewTimer(5 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		exited := false
	waitLoop:
		for {
			if syscall.Kill(pid, 0) != nil {
				exited = true
				break
			}
			select {
			case <-ctx.Done():
				break waitLoop
			case <-deadline.C:
				break waitLoop
			case <-ticker.C:
			}
		}
		ticker.Stop()
		if !deadline.Stop() {
			select {
			case <-deadline.C:
			default:
			}
		}

		if !exited && syscall.Kill(pid, 0) == nil {
			if pgid > 0 {
				if killErr := syscall.Kill(-pgid, syscall.SIGKILL); killErr != nil && killErr != syscall.ESRCH {
					slog.Warn("failed to SIGKILL process group", "pid", pid, "pgid", pgid, "error", killErr)
				}
			} else {
				if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil && killErr != syscall.ESRCH {
					slog.Warn("failed to SIGKILL process", "pid", pid, "error", killErr)
				}
			}
		}
	}

	if p.statusStore != nil {
		_ = p.statusStore.DeleteStatus(ctx, agent.GuildID, agent.ID)
		_ = p.statusStore.DeleteStatus(ctx, unknownGuildKey, agent.ID)
	}
	p.stopNetworkRelay(guildID, agentID)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent %s stop deadline: %w", agentID, err)
	}
	return nil
}

func (p *BubblewrapSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.RLock()
	agent, exists := p.agents[key]
	p.mu.RUnlock()

	if !exists {
		return "unknown", nil
	}
	return string(agent.GetState()), nil
}

func (p *BubblewrapSupervisor) GetPID(ctx context.Context, guildID, agentID string) (int, error) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.RLock()
	agent, exists := p.agents[key]
	p.mu.RUnlock()

	if !exists {
		return 0, fmt.Errorf("agent %s not managed in guild %s", agentID, normalizeGuildID(guildID))
	}

	return agent.GetPID(), nil
}

func (p *BubblewrapSupervisor) StopAll(ctx context.Context) error {
	if p == nil {
		return nil
	}

	p.mu.RLock()
	agents := make([]*ManagedAgent, 0, len(p.agents))
	for _, agent := range p.agents {
		agents = append(agents, agent)
	}
	p.mu.RUnlock()

	var wg sync.WaitGroup
	errs := make(chan error, len(agents))
	for _, agent := range agents {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Stop(ctx, agent.GuildID, agent.ID); err != nil {
				slog.Warn("failed to stop agent", "guild_id", agent.GuildID, "agent_id", agent.ID, "error", err)
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	var joined []error
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (p *BubblewrapSupervisor) setBridge(guildID, agentID string, bridge *AgentMessagingBridge) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bridges[key] = bridge
}

func (p *BubblewrapSupervisor) stopBridge(guildID, agentID string) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.Lock()
	bridge := p.bridges[key]
	delete(p.bridges, key)
	p.mu.Unlock()
	if bridge != nil {
		bridge.Close()
	}
}

func (p *BubblewrapSupervisor) setNetworkRelay(guildID, agentID string, relay *AgentNetworkRelay) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.networkRelays[key] = relay
}

func (p *BubblewrapSupervisor) stopNetworkRelay(guildID, agentID string) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.Lock()
	relay := p.networkRelays[key]
	systemTCPRelay := p.systemTCPRelays[key]
	delete(p.networkRelays, key)
	delete(p.systemTCPRelays, key)
	p.mu.Unlock()
	if relay != nil {
		_ = relay.Close()
	}
	if systemTCPRelay != nil {
		_ = systemTCPRelay.Close()
	}
}

func (p *BubblewrapSupervisor) setSystemTCPRelay(guildID, agentID string, relay *AgentTCPRelay) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.systemTCPRelays[key] = relay
}

func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

func bubblewrapWritablePaths(homeDir string, env []string) []string {
	paths := []string{
		filepath.Join(homeDir, ".local", "share", "uv"),
		filepath.Join(homeDir, ".cache", "uv"),
		filepath.Join(homeDir, ".forge"),
	}

	envMap := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			envMap[key] = value
		}
	}

	for _, key := range []string{"FORGE_UV_CACHE_DIR", "UV_CACHE_DIR", "XDG_CACHE_HOME", "XDG_DATA_HOME", "TMPDIR"} {
		value := strings.TrimSpace(envMap[key])
		if value == "" && key != "TMPDIR" {
			value = strings.TrimSpace(os.Getenv(key))
		}
		if value := filepath.Clean(value); filepath.IsAbs(value) && value != "/" && value != "." {
			paths = append(paths, value)
		}
	}

	seen := make(map[string]struct{}, len(paths))
	deduped := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		deduped = append(deduped, path)
	}

	return deduped
}
