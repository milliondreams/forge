package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/stretchr/testify/require"
)

type stubSupervisor struct {
	launched bool
}

func (s *stubSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	_ = ctx
	_ = guildID
	_ = agentSpec
	_ = reg
	_ = env
	s.launched = true
	return nil
}

func (s *stubSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	_ = ctx
	_ = guildID
	_ = agentID
	return nil
}

func (s *stubSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	_ = ctx
	_ = guildID
	_ = agentID
	return "running", nil
}

func (s *stubSupervisor) StopAll(ctx context.Context) error {
	_ = ctx
	return nil
}

func loadDispatcherTestRegistry(t *testing.T, content string) *registry.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	reg, err := registry.Load(path, nil)
	require.NoError(t, err)
	return reg
}

func TestDispatchingSupervisorAllowsSupervisorZMQForNonProcessRuntime(t *testing.T) {
	reg := loadDispatcherTestRegistry(t, `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: docker
    image: "busybox:latest"
`)

	dockerSup := &stubSupervisor{}
	dispatcher := NewDispatchingSupervisor("", "direct", nil, dockerSup, nil)

	err := dispatcher.Launch(
		context.Background(),
		"guild-1",
		&protocol.AgentSpec{ID: "agent-1", ClassName: "test.Agent"},
		reg,
		[]string{protocol.EnvForgeAgentTransport + "=" + string(protocol.AgentTransportSupervisorZMQ)},
	)
	require.NoError(t, err)
	require.True(t, dockerSup.launched)
}

func TestDispatchingSupervisorDoesNotFallbackWhenRequiredDefaultMissing(t *testing.T) {
	dispatcher := NewDispatchingSupervisor("bwrap", "supervisor-zmq", &stubSupervisor{}, nil, nil)
	_, err := dispatcher.selectSupervisor(&registry.AgentRegistryEntry{Runtime: registry.RuntimeUVX})
	require.ErrorContains(t, err, "required default supervisor bwrap is unavailable")
}

func TestDispatchingSupervisorRejectsContainerRuntimesInAgentOSMode(t *testing.T) {
	dispatcher := NewDispatchingSupervisor(
		"bwrap",
		"supervisor-zmq",
		&stubSupervisor{},
		&stubSupervisor{},
		&stubSupervisor{},
		WithDispatchingAgentOSMode(true),
	)

	for _, runtime := range []registry.RuntimeType{registry.RuntimeDocker, "podman", "Docker"} {
		_, err := dispatcher.selectSupervisor(&registry.AgentRegistryEntry{Runtime: runtime})
		require.ErrorContains(t, err, "not permitted in AgentOS mode")
	}
}

func TestDispatchingSupervisorPreservesContainerRuntimeInHostMode(t *testing.T) {
	dockerSupervisor := &stubSupervisor{}
	dispatcher := NewDispatchingSupervisor("", "direct", nil, dockerSupervisor, nil)

	selected, err := dispatcher.selectSupervisor(&registry.AgentRegistryEntry{Runtime: registry.RuntimeDocker})
	require.NoError(t, err)
	require.Same(t, dockerSupervisor, selected)
}
