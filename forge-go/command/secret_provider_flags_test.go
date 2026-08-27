package command

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestSecretProviderFlagsUseSecureDefaults(t *testing.T) {
	serverFlag := ServerCmd.Flags().Lookup("secret-providers")
	if serverFlag == nil || serverFlag.DefValue != "keychain" {
		t.Fatalf("server --secret-providers default = %#v", serverFlag)
	}
	clientFlag := ClientCmd.Flags().Lookup("secret-providers")
	if clientFlag == nil || clientFlag.DefValue != "keychain" {
		t.Fatalf("client --secret-providers default = %#v", clientFlag)
	}
	if ClientCmd.Flags().Lookup("dependency-config") == nil {
		t.Fatal("standalone client must expose --dependency-config")
	}
}

func TestExplicitEmptySecretProviderFlagIsRejected(t *testing.T) {
	cmd := &cobra.Command{}
	var value string
	cmd.Flags().StringVar(&value, "secret-providers", "keychain", "")
	if err := cmd.Flags().Set("secret-providers", ""); err != nil {
		t.Fatal(err)
	}
	if err := validateSecretProviderFlag(cmd, value); err == nil {
		t.Fatal("explicit empty provider chain must be rejected")
	}
}
