//go:build windows

package supervisor

import (
	"context"
	"errors"
	"sync"
)

type DependencyStatus struct {
	Phase                  string `json:"phase"`
	SystemEnvironmentReady bool   `json:"systemEnvironmentReady"`
	ActivePreparations     int    `json:"activePreparations"`
	LastError              string `json:"lastError,omitempty"`
}

type DependencyRequest struct {
	Requirements []string
}

type DependencyEnvironment struct {
	Path    string
	Key     string
	release func()
	once    sync.Once
}

func (e *DependencyEnvironment) Release() {
	if e != nil && e.release != nil {
		e.once.Do(e.release)
	}
}

type DependencyMaterializerConfig struct{}
type DependencyMaterializer struct{}

func NewDependencyMaterializer(DependencyMaterializerConfig) (*DependencyMaterializer, error) {
	return nil, errors.New("AgentOS dependency materialization is not supported on Windows guests")
}

func NewDependencyMaterializerFromEnvironment() (*DependencyMaterializer, error) {
	return nil, errors.New("AgentOS dependency materialization is not supported on Windows guests")
}

func (m *DependencyMaterializer) SetStatusNotify(func(DependencyStatus)) {}
func (m *DependencyMaterializer) Status() DependencyStatus {
	return DependencyStatus{Phase: "unsupported"}
}
func (m *DependencyMaterializer) WarmSystem(context.Context) {}
func (m *DependencyMaterializer) Prepare(context.Context, DependencyRequest) (*DependencyEnvironment, error) {
	return nil, errors.New("AgentOS dependency materialization is not supported on Windows guests")
}
func (m *DependencyMaterializer) ClearInactive() error {
	return errors.New("AgentOS dependency materialization is not supported on Windows guests")
}
