package guild

import (
	"github.com/rustic-ai/forge/forge-go/localmodel"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func applyLocalModelEndpointOverride(spec *protocol.GuildSpec) error {
	if err := localmodel.ApplyDependencyOverride(spec.DependencyMap); err != nil {
		return err
	}
	for index := range spec.Agents {
		if err := localmodel.ApplyDependencyOverride(spec.Agents[index].DependencyMap); err != nil {
			return err
		}
	}
	return nil
}
