package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

type DispatchingSupervisor struct {
	nodeDefault   string
	nodeTransport string
	processSup    AgentSupervisor
	dockerSup     AgentSupervisor
	bwrapSup      AgentSupervisor
	agentOSMode   bool

	mu        sync.RWMutex
	ownership map[string]AgentSupervisor
}

type DispatchingSupervisorOption func(*DispatchingSupervisor)

func WithDispatchingAgentOSMode(enabled bool) DispatchingSupervisorOption {
	return func(dispatcher *DispatchingSupervisor) {
		dispatcher.agentOSMode = enabled
	}
}

func NewDispatchingSupervisor(
	nodeDefault string,
	nodeTransport string,
	process AgentSupervisor,
	docker AgentSupervisor,
	bwrap AgentSupervisor,
	options ...DispatchingSupervisorOption,
) *DispatchingSupervisor {
	dispatcher := &DispatchingSupervisor{
		nodeDefault:   nodeDefault,
		nodeTransport: nodeTransport,
		processSup:    process,
		dockerSup:     docker,
		bwrapSup:      bwrap,
		ownership:     make(map[string]AgentSupervisor),
	}
	for _, option := range options {
		option(dispatcher)
	}
	return dispatcher
}

func hasSupervisor(sup AgentSupervisor) bool {
	if sup == nil {
		return false
	}

	value := reflect.ValueOf(sup)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (d *DispatchingSupervisor) selectSupervisor(entry *registry.AgentRegistryEntry) (AgentSupervisor, error) {
	if d.agentOSMode && isContainerRuntime(entry.Runtime) {
		return nil, fmt.Errorf("container runtime %q is not permitted in AgentOS mode", entry.Runtime)
	}

	switch d.nodeDefault {
	case "":
	case "docker":
		if hasSupervisor(d.dockerSup) {
			return d.dockerSup, nil
		}
		return nil, fmt.Errorf("required default supervisor docker is unavailable")
	case "bwrap":
		if hasSupervisor(d.bwrapSup) {
			return d.bwrapSup, nil
		}
		return nil, fmt.Errorf("required default supervisor bwrap is unavailable")
	case "process":
		if hasSupervisor(d.processSup) {
			return d.processSup, nil
		}
		return nil, fmt.Errorf("required default supervisor process is unavailable")
	default:
		return nil, fmt.Errorf("unknown default supervisor %q", d.nodeDefault)
	}

	requested := entry.Runtime
	if requested == registry.RuntimeDocker && hasSupervisor(d.dockerSup) {
		return d.dockerSup, nil
	}
	if requested == registry.RuntimeDocker {
		return nil, fmt.Errorf("requested docker supervisor is unavailable")
	}
	if requested == "bwrap" && hasSupervisor(d.bwrapSup) {
		return d.bwrapSup, nil
	}
	if requested == "bwrap" {
		return nil, fmt.Errorf("requested bwrap supervisor is unavailable")
	}

	if hasSupervisor(d.processSup) {
		return d.processSup, nil
	}

	return nil, fmt.Errorf("no suitable supervisor found for requested runtime: %s", requested)
}

func isContainerRuntime(runtime registry.RuntimeType) bool {
	switch strings.ToLower(strings.TrimSpace(string(runtime))) {
	case "docker", "podman":
		return true
	default:
		return false
	}
}

func (d *DispatchingSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	log := logging.FromContext(ctx, slog.Default())

	entry, err := reg.Lookup(agentSpec.ClassName)
	if err != nil {
		return err
	}

	sup, err := d.selectSupervisor(entry)
	if err != nil {
		return err
	}

	log.Debug("Dispatching agent launch", "agent_id", agentSpec.ID, "runtime", entry.Runtime, "supervisor", fmt.Sprintf("%T", sup))

	if err := sup.Launch(ctx, guildID, agentSpec, reg, env); err != nil {
		return err
	}

	key := scopedAgentKey(guildID, agentSpec.ID)
	d.mu.Lock()
	d.ownership[key] = sup
	d.mu.Unlock()

	return nil
}

func (d *DispatchingSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	key := scopedAgentKey(guildID, agentID)
	d.mu.RLock()
	sup, exists := d.ownership[key]
	d.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not found in any supervisor for guild %s", agentID, normalizeGuildID(guildID))
	}

	err := sup.Stop(ctx, guildID, agentID)
	if err == nil {
		d.mu.Lock()
		delete(d.ownership, key)
		d.mu.Unlock()
	}
	return err
}

func (d *DispatchingSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	key := scopedAgentKey(guildID, agentID)
	d.mu.RLock()
	sup, exists := d.ownership[key]
	d.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found in any supervisor for guild %s", agentID, normalizeGuildID(guildID))
	}

	return sup.Status(ctx, guildID, agentID)
}

func (d *DispatchingSupervisor) StopAll(ctx context.Context) error {
	var errs []error
	for _, sup := range []AgentSupervisor{d.processSup, d.dockerSup, d.bwrapSup} {
		if hasSupervisor(sup) {
			if err := sup.StopAll(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}

	d.mu.Lock()
	d.ownership = make(map[string]AgentSupervisor)
	d.mu.Unlock()

	return errors.Join(errs...)
}

func resolvedTransportFromEnv(env []string, defaultTransport string) protocol.AgentTransportMode {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, protocol.EnvForgeAgentTransport+"="); ok {
			return protocol.NormalizeAgentTransportMode(value)
		}
	}
	return protocol.NormalizeAgentTransportMode(defaultTransport)
}
