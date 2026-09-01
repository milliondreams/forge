package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	gopsprocess "github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/require"
)

func getSleepCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"ping", "-n", "10", "127.0.0.1"}
	}
	return []string{"sleep", "10"}
}

func getEchoCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", "echo", "hello"}
	}
	return []string{"echo", "hello"}
}

func getWorkDirProbeCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{
			"cmd",
			"/C",
			`cd > cwd.txt & (echo %FORGE_AGENT_WORKDIR%& echo %HOME%& echo %TMP%& echo %XDG_CACHE_HOME%& echo %XDG_DATA_HOME%& echo %USERPROFILE%) > env.txt & ping -n 10 127.0.0.1 >NUL`,
		}
	}
	return []string{
		"sh",
		"-c",
		`pwd > cwd.txt; printf "%s\n%s\n%s\n%s\n%s\n%s\n" "$FORGE_AGENT_WORKDIR" "$HOME" "$TMPDIR" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$USERPROFILE" > env.txt; sleep 10`,
	}
}

func getChildTreeCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", "ping -n 10 127.0.0.1 >NUL"}
	}
	return []string{"sh", "-c", `sleep 30 & echo $! > child.pid; wait`}
}

func getStubbornChildTreeCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", "ping -n 30 127.0.0.1 >NUL"}
	}
	return []string{
		"sh",
		"-c",
		`(trap '' TERM; while :; do sleep 1; done) & echo $! > child.pid; wait`,
	}
}

func getGracefulTerminationCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", "ping -n 30 127.0.0.1 >NUL"}
	}
	return []string{
		"sh",
		"-c",
		`trap 'echo terminated > term.txt; exit 0' TERM; touch ready.txt; while :; do sleep 1; done`,
	}
}

type recordingInfraBackend struct {
	mu       sync.Mutex
	messages map[string][]protocol.Message
}

func newRecordingInfraBackend() *recordingInfraBackend {
	return &recordingInfraBackend{messages: make(map[string][]protocol.Message)}
}

func (r *recordingInfraBackend) PublishMessage(_ context.Context, namespace, topic string, msg *protocol.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages[namespace+":"+topic] = append(r.messages[namespace+":"+topic], *msg)
	return nil
}

func (r *recordingInfraBackend) GetMessagesForTopic(_ context.Context, namespace, topic string) ([]protocol.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Message(nil), r.messages[namespace+":"+topic]...), nil
}

func (r *recordingInfraBackend) GetMessagesSince(_ context.Context, _, _ string, _ uint64) ([]protocol.Message, error) {
	return nil, nil
}

func (r *recordingInfraBackend) GetMessagesByID(_ context.Context, _ string, _ []uint64) ([]protocol.Message, error) {
	return nil, nil
}

func (r *recordingInfraBackend) Subscribe(_ context.Context, _ string, _ ...string) (messaging.Subscription, error) {
	return nil, nil
}

func (r *recordingInfraBackend) DeleteNamespace(_ context.Context, _ string) error { return nil }

func (r *recordingInfraBackend) Close() error { return nil }

func loadProcessEventKinds(t *testing.T, backend *recordingInfraBackend, guildID string) []string {
	t.Helper()
	msgs, err := backend.GetMessagesForTopic(context.Background(), guildID, infraevents.Topic)
	require.NoError(t, err)
	kinds := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		var event infraevents.Event
		require.NoError(t, json.Unmarshal(msg.Payload, &event))
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func TestProcessSupervisorLaunchAndStop(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()))
	ctx := context.Background()
	guildID := "test-guild"

	agent1 := NewManagedAgent(guildID, "agent1")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent1")] = agent1
	sup.mu.Unlock()

	err := sup.startProcess(ctx, guildID, agent1, &protocol.AgentSpec{}, getSleepCmd(), []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("Failed to launch process: %v", err)
	}

	status, err := sup.Status(ctx, guildID, "agent1")
	if err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}
	if status != string(StateRunning) {
		t.Errorf("Expected status to be running, got %s", status)
	}

	// Wait briefly so process spawns
	time.Sleep(100 * time.Millisecond)

	err = sup.Stop(ctx, guildID, "agent1")
	if err != nil {
		t.Fatalf("Failed to stop process: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	status, _ = sup.Status(ctx, guildID, "agent1")
	if status != string(StateStopped) {
		t.Errorf("Expected status to be stopped, got %s", status)
	}
}

func TestProcessSupervisorLaunchContextDoesNotOwnWorkloadLifetime(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()), WithAttachedProcessTree())
	ctx, cancel := context.WithCancel(context.Background())
	guildID := "guild-launch-context"
	agentID := "agent-launch-context"
	agent := NewManagedAgent(guildID, agentID)
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, agentID)] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, getSleepCmd(), nil))
	pid := agent.GetPID()
	require.Positive(t, pid)

	cancel()
	time.Sleep(200 * time.Millisecond)
	require.True(t, processExists(pid), "canceling the spawn context must not stop a supervisor-owned workload")

	require.NoError(t, sup.Stop(context.Background(), guildID, agentID))
	require.False(t, processExists(pid))
}

func TestProcessSupervisorCrashRestart(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()))
	ctx := context.Background()
	guildID := "test-guild"

	// process that exits immediately
	agentCrash := NewManagedAgent(guildID, "agent-crash")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent-crash")] = agentCrash
	sup.mu.Unlock()

	err := sup.startProcess(ctx, guildID, agentCrash, &protocol.AgentSpec{}, getEchoCmd(), nil)
	if err != nil {
		t.Fatalf("Failed to launch process: %v", err)
	}

	// Wait for the process tree's stable-absence confirmation and for the
	// background monitor to enter its bounded restart delay.
	require.Eventually(t, func() bool {
		status, _ := sup.Status(ctx, guildID, "agent-crash")
		return status == string(StateRestarting)
	}, 2*time.Second, 25*time.Millisecond)

	require.NoError(t, sup.Stop(ctx, guildID, "agent-crash"))
	time.Sleep(200 * time.Millisecond)

	status, _ := sup.Status(ctx, guildID, "agent-crash")
	if status != string(StateStopped) {
		t.Errorf("Expected status to be stopped after Stop(), got %s", status)
	}
}

func TestProcessSupervisorGuildScopedAgentKeys(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()))
	ctx := context.Background()

	agentID := "upa-dummyuserid"
	guildA := "guild-a"
	guildB := "guild-b"

	a := NewManagedAgent(guildA, agentID)
	a.SetState(StateRunning)
	b := NewManagedAgent(guildB, agentID)
	b.SetState(StateStopped)

	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildA, agentID)] = a
	sup.agents[scopedAgentKey(guildB, agentID)] = b
	sup.mu.Unlock()

	statusA, err := sup.Status(ctx, guildA, agentID)
	if err != nil {
		t.Fatalf("status for guild A failed: %v", err)
	}
	if statusA != string(StateRunning) {
		t.Fatalf("expected guild A status running, got %s", statusA)
	}

	statusB, err := sup.Status(ctx, guildB, agentID)
	if err != nil {
		t.Fatalf("status for guild B failed: %v", err)
	}
	if statusB != string(StateStopped) {
		t.Fatalf("expected guild B status stopped, got %s", statusB)
	}
}

func TestProcessSupervisorLaunchesIntoPerAgentWorkDir(t *testing.T) {
	baseDir := t.TempDir()
	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(baseDir),
		WithOrganizationID("org-1"),
	)
	ctx := context.Background()
	guildID := "guild-1"

	agent := NewManagedAgent(guildID, "agent-1")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent-1")] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, getWorkDirProbeCmd(), nil))
	defer func() {
		_ = sup.Stop(ctx, guildID, "agent-1")
	}()

	workDir := sup.resolveAgentWorkDir(guildID, "agent-1")

	var cwdContent string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(workDir, "cwd.txt"))
		if err != nil {
			return false
		}
		cwdContent = strings.TrimSpace(string(data))
		return cwdContent != ""
	}, 5*time.Second, 50*time.Millisecond)
	canonicalWorkDir, err := filepath.EvalSymlinks(workDir)
	require.NoError(t, err)
	canonicalCWD, err := filepath.EvalSymlinks(cwdContent)
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(canonicalWorkDir), filepath.Clean(canonicalCWD))

	var lines []string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(workDir, "env.txt"))
		if err != nil {
			return false
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			return false
		}
		lines = strings.Split(trimmed, "\n")
		return len(lines) >= 6
	}, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(lines[0])))
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(lines[1])))
	require.Equal(t, filepath.Clean(filepath.Join(workDir, "tmp")), filepath.Clean(strings.TrimSpace(lines[2])))
	require.Equal(t, filepath.Clean(filepath.Join(workDir, ".cache")), filepath.Clean(strings.TrimSpace(lines[3])))
	require.Equal(t, filepath.Clean(filepath.Join(workDir, ".local", "share")), filepath.Clean(strings.TrimSpace(lines[4])))
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(lines[5])))
}

func TestProcessSupervisorAttachedProcessTreeStopsSubprocesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("attached process tree semantics are only asserted on unix in this test")
	}

	baseDir := t.TempDir()
	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(baseDir),
		WithAttachedProcessTree(),
	)
	require.False(t, sup.detachGroup)

	ctx := context.Background()
	guildID := "guild-attach"
	agentID := "agent-attach"

	agent := NewManagedAgent(guildID, agentID)
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, agentID)] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, getChildTreeCmd(), nil))

	workDir := sup.resolveAgentWorkDir(guildID, agentID)
	childPIDPath := filepath.Join(workDir, "child.pid")
	childPID := 0
	require.Eventually(t, func() bool {
		childPIDRaw, err := os.ReadFile(childPIDPath)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(childPIDRaw)))
		if err != nil || pid <= 0 {
			return false
		}
		childPID = pid
		return true
	}, 5*time.Second, 50*time.Millisecond)

	childAlive, err := gopsprocess.PidExists(int32(childPID))
	require.NoError(t, err)
	require.True(t, childAlive)

	require.NoError(t, sup.Stop(ctx, guildID, agentID))

	require.Eventually(t, func() bool {
		alive, err := gopsprocess.PidExists(int32(childPID))
		return err == nil && !alive
	}, 5*time.Second, 50*time.Millisecond)

	status, err := sup.Status(ctx, guildID, agentID)
	require.NoError(t, err)
	require.Equal(t, string(StateStopped), status)
}

func TestProcessSupervisorWaitsForProcessGroupBeforeReportingStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process-group behavior")
	}

	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(t.TempDir()),
		WithAttachedProcessTree(),
		withProcessTerminationTimeouts(400*time.Millisecond, time.Second),
	)
	guildID := "guild-stubborn"
	agentID := "agent-stubborn"
	agent := NewManagedAgent(guildID, agentID)
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, agentID)] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(context.Background(), guildID, agent, &protocol.AgentSpec{}, getStubbornChildTreeCmd(), nil))
	workDir := sup.resolveAgentWorkDir(guildID, agentID)
	childPIDPath := filepath.Join(workDir, "child.pid")
	childPID := 0
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(childPIDPath)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && childPID > 0
	}, 5*time.Second, 50*time.Millisecond)

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- sup.Stop(context.Background(), guildID, agentID)
	}()
	time.Sleep(150 * time.Millisecond)
	require.NotEqual(t, StateStopped, agent.GetState())
	require.True(t, processExists(childPID))

	require.NoError(t, <-stopDone)
	require.False(t, processExists(childPID))
	require.Equal(t, StateStopped, agent.GetState())
	require.Equal(t, 0, agent.GetPID())
	require.Equal(t, 0, agent.GetProcessGroupID())
	time.Sleep(250 * time.Millisecond)
	require.Equal(t, 0, agent.RestartCount)
}

func TestProcessSupervisorStopsAgentsConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process-group behavior")
	}

	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(t.TempDir()),
		WithAttachedProcessTree(),
		withProcessTerminationTimeouts(500*time.Millisecond, time.Second),
	)
	for _, agentID := range []string{"agent-one", "agent-two"} {
		agent := NewManagedAgent("guild-concurrent", agentID)
		sup.mu.Lock()
		sup.agents[scopedAgentKey("guild-concurrent", agentID)] = agent
		sup.mu.Unlock()
		require.NoError(t, sup.startProcess(context.Background(), "guild-concurrent", agent, &protocol.AgentSpec{}, getStubbornChildTreeCmd(), nil))
		childPIDPath := filepath.Join(sup.resolveAgentWorkDir("guild-concurrent", agentID), "child.pid")
		require.Eventually(t, func() bool {
			_, err := os.Stat(childPIDPath)
			return err == nil
		}, 5*time.Second, 50*time.Millisecond)
	}

	startedAt := time.Now()
	require.NoError(t, sup.StopAll(context.Background()))
	require.Less(t, time.Since(startedAt), 900*time.Millisecond)
}

func TestProcessSupervisorAllowsGracefulExitBeforeKillDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix signal behavior")
	}

	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(t.TempDir()),
		WithAttachedProcessTree(),
		withProcessTerminationTimeouts(time.Second, time.Second),
	)
	guildID := "guild-graceful"
	agentID := "agent-graceful"
	agent := NewManagedAgent(guildID, agentID)
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, agentID)] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(context.Background(), guildID, agent, &protocol.AgentSpec{}, getGracefulTerminationCmd(), nil))
	workDir := sup.resolveAgentWorkDir(guildID, agentID)
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(workDir, "ready.txt"))
		return err == nil
	}, 5*time.Second, 25*time.Millisecond)
	require.NoError(t, sup.Stop(context.Background(), guildID, agentID))
	_, err := os.Stat(filepath.Join(workDir, "term.txt"))
	require.NoError(t, err)
}

func TestProcessSupervisorEmitsLifecycleInfraEvents(t *testing.T) {
	backend := newRecordingInfraBackend()
	pub, err := infraevents.NewPublisher(backend)
	require.NoError(t, err)

	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()), WithInfraEventPublisher(pub))
	ctx := context.Background()
	guildID := "test-guild"

	agent := NewManagedAgent(guildID, "agent1")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent1")] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, getSleepCmd(), nil))
	require.Eventually(t, func() bool {
		kinds := loadProcessEventKinds(t, backend, guildID)
		return len(kinds) >= 2
	}, 2*time.Second, 20*time.Millisecond)
	require.NoError(t, sup.Stop(ctx, guildID, "agent1"))
	require.Eventually(t, func() bool {
		kinds := loadProcessEventKinds(t, backend, guildID)
		return len(kinds) >= 3 && kinds[len(kinds)-1] == "agent.process.stopped"
	}, 3*time.Second, 20*time.Millisecond)

	require.Equal(t, []string{
		"agent.process.starting",
		"agent.process.started",
		"agent.process.stopped",
	}, loadProcessEventKinds(t, backend, guildID))
}

func TestProcessSupervisorEmitsFailureInfraEvents(t *testing.T) {
	backend := newRecordingInfraBackend()
	pub, err := infraevents.NewPublisher(backend)
	require.NoError(t, err)

	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()), WithInfraEventPublisher(pub))
	ctx := context.Background()
	guildID := "test-guild"

	agent := NewManagedAgent(guildID, "agent-bad")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent-bad")] = agent
	sup.mu.Unlock()

	err = sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, []string{"/definitely/not/a/command"}, nil)
	require.Error(t, err)
	require.NoError(t, sup.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.failed", infraevents.SeverityError, "agent process failed before startup completed", nil, map[string]any{"error": err.Error()}))

	require.Equal(t, []string{
		"agent.process.starting",
		"agent.process.start_failed",
		"agent.process.failed",
	}, loadProcessEventKinds(t, backend, guildID))
}
