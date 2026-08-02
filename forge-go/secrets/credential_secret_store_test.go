package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/credentials"
)

func TestCredentialSecretStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.OpenVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()

	store := NewCredentialSecretStore(vault)
	for _, name := range []string{"B", "A"} {
		if err := store.Save("org", name, strings.ToLower(name)); err != nil {
			t.Fatal(err)
		}
	}
	if names, err := store.List("org"); err != nil || strings.Join(names, ",") != "A,B" {
		t.Fatalf("List() = %v, %v", names, err)
	}
	if !store.Exists("org", "A") {
		t.Fatal("saved credential does not exist")
	}
	if deleted, err := store.Delete("org", "A"); err != nil || !deleted {
		t.Fatalf("Delete() = %v, %v", deleted, err)
	}
}
