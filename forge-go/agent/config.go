package agent

import (
	"time"

	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

type ServerConfig struct {
	DatabaseURL             string
	RedisURL                string
	NATSUrl                 string
	Backend                 string // "redis" (default) or "nats"
	EmbeddedRedisAddr       string
	EmbeddedNATSAddr        string // Bind address for embedded NATS (default: ephemeral)
	ListenAddress           string
	ManagerAPIBaseURL       string
	DataDir                 string
	DependencyConfig        string
	WithClient              bool
	ClientNodeID            string
	ClientMetricsAddr       string
	ClientCPUs              int
	ClientMemory            int
	ClientGPUs              int
	ClientDefaultSupervisor string
	ClientDefaultTransport  string
	ClientZMQBridgeMode     string
	ClientAttachProcessTree bool
	LeaderElectionMode      string
	RaftBindAddr            string
	GossipBindAddr          string
	GossipJoinPeers         []string
	StateStore              string
	TelemetryEnabled        bool
	TelemetryMode           string
	TelemetryEndpoint       string
	TelemetryServiceName    string
	TelemetrySQLiteBinary   string
	TelemetrySQLiteDBPath   string
	TelemetrySQLitePort     int
	AgentOSMode             bool
	AgentOSStateSchema      int
	ShutdownTimeout         time.Duration
}

type ClientConfig struct {
	ServerURL          string
	RedisURL           string
	NATSUrl            string
	DataDir            string
	CPUs               int
	Memory             int
	GPUs               int
	NodeID             string
	MetricsAddr        string
	DefaultSupervisor  string
	DefaultTransport   string
	ZMQBridgeMode      string
	AttachProcessTree  bool
	StopAgentsOnExit   bool
	OAuthManager       *oauth.Manager
	SecretProvider     secrets.SecretProvider
	AgentOSMode        bool
	ShutdownTimeout    time.Duration
	OnReady            func()
	OnDependencyStatus func(supervisor.DependencyStatus)
	OnDependencyCache  func(func() error)
}
