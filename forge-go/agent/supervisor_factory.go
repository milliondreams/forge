package agent

import (
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

func buildOrgSupervisorFactory(
	statusStore supervisor.AgentStatusStore,
	managerAPIBaseURL string,
	systemRedisAddress string,
	defaultSupervisor string,
	defaultTransport string,
	msgBackend messaging.Backend,
	infraPublisher *infraevents.Publisher,
	dataDir string,
	attachProcessTree bool,
	zmqBridgeMode string,
	agentOSMode bool,
	materializer *supervisor.DependencyMaterializer,
) control.SupervisorFactory {
	bridgeMode := supervisor.NormalizeBridgeTransportMode(zmqBridgeMode)

	return func(orgID string) supervisor.AgentSupervisor {
		opts := []supervisor.ProcessSupervisorOption{
			supervisor.WithOrganizationID(orgID),
			supervisor.WithWorkDirBase(dataDir),
			supervisor.WithDefaultAgentTransport(defaultTransport),
			supervisor.WithMessagingBackend(msgBackend),
			supervisor.WithInfraEventPublisher(infraPublisher),
		}
		if attachProcessTree {
			opts = append(opts, supervisor.WithAttachedProcessTree())
		}
		processSup := supervisor.NewProcessSupervisor(statusStore, opts...)

		var dockerSup *supervisor.DockerSupervisor
		if !agentOSMode {
			if ds, err := supervisor.NewDockerSupervisor(statusStore,
				supervisor.WithDockerDefaultTransport(defaultTransport),
				supervisor.WithDockerMessagingBackend(msgBackend),
				supervisor.WithDockerZMQBridgeMode(bridgeMode),
			); err == nil && ds.Available() {
				dockerSup = ds
			}
		}

		var bwrapSup *supervisor.BubblewrapSupervisor
		bubblewrapOptions := []supervisor.BubblewrapSupervisorOption{
			supervisor.WithBubblewrapDefaultTransport(defaultTransport),
			supervisor.WithBubblewrapMessagingBackend(msgBackend),
			supervisor.WithBubblewrapZMQBridgeMode(bridgeMode),
			supervisor.WithBubblewrapAgentOSMode(agentOSMode),
		}
		if agentOSMode {
			bubblewrapOptions = append(bubblewrapOptions,
				supervisor.WithBubblewrapDependencyMaterializer(materializer),
				supervisor.WithBubblewrapManagerAPIBaseURL(managerAPIBaseURL),
				supervisor.WithBubblewrapSystemRedisAddress(systemRedisAddress),
			)
		}
		bs := supervisor.NewBubblewrapSupervisor(statusStore, bubblewrapOptions...)
		if bs.Available() {
			bwrapSup = bs
		}

		return supervisor.NewDispatchingSupervisor(
			defaultSupervisor,
			defaultTransport,
			processSup,
			dockerSup,
			bwrapSup,
			supervisor.WithDispatchingAgentOSMode(agentOSMode),
		)
	}
}
