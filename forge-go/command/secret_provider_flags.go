package command

import (
	"fmt"
	"strings"

	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/spf13/cobra"
)

func validateSecretProviderFlag(cmd *cobra.Command, value string) error {
	if cmd.Flags().Changed("secret-providers") && strings.TrimSpace(value) == "" {
		return fmt.Errorf("--secret-providers must not be empty")
	}
	_, _, err := secrets.ParseProviderChain(value)
	return err
}
