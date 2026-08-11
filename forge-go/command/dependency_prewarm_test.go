package command

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestDependencyPrewarmFlagsDefaultOff(t *testing.T) {
	for _, command := range []*cobra.Command{ClientCmd, ServerCmd} {
		flag := command.Flags().Lookup("client-dependency-prewarm")
		if flag == nil {
			t.Fatalf("%s command is missing --client-dependency-prewarm", command.Name())
		}
		if flag.DefValue != "off" {
			t.Fatalf("%s dependency prewarm default = %q, want off", command.Name(), flag.DefValue)
		}
	}
}
