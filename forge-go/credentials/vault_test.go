package credentials

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newVaultForTest(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, privateDirMode); err != nil {
		t.Fatal(err)
	}
	vault, err := OpenVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vault.Close)
	return vault, dir
}

func TestVaultPersistsEncryptedCredentials(t *testing.T) {
	vault, dir := newVaultForTest(t)
	secret := "not-present-in-ciphertext"
	if err := vault.Put("secret:org|API_KEY", secret); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, vaultFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("vault contains plaintext secret")
	}
	vault.Close()
	reopened, err := OpenVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if value, ok := reopened.Get("secret:org|API_KEY"); !ok || value != secret {
		t.Fatalf("unexpected reopened credential: %q, %v", value, ok)
	}
	if deleted, err := reopened.Delete("secret:org|API_KEY"); err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
}

func TestVaultListIsSortedAndConcurrent(t *testing.T) {
	vault, _ := newVaultForTest(t)
	var wg sync.WaitGroup
	for _, key := range []string{"secret:org|C", "secret:org|A", "secret:org|B"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := vault.Put(key, key); err != nil {
				t.Errorf("put %s: %v", key, err)
			}
		}()
	}
	wg.Wait()
	got := vault.List("secret:org|")
	want := []string{"secret:org|A", "secret:org|B", "secret:org|C"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("list=%v want=%v", got, want)
	}
}

func TestVaultRejectsTampering(t *testing.T) {
	vault, dir := newVaultForTest(t)
	if err := vault.Put("secret:org|A", "value"); err != nil {
		t.Fatal(err)
	}
	vault.Close()
	path := filepath.Join(dir, vaultFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document vaultDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(document.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-1] ^= 1
	document.Ciphertext = base64.RawStdEncoding.EncodeToString(ciphertext)
	data, _ = json.Marshal(document)
	if err := os.WriteFile(path, data, privateFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(dir); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected authentication failure, got %v", err)
	}
}

func TestVaultRejectsUnsafePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(dir); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("expected permission failure, got %v", err)
	}
}

func TestVaultRecoversInterruptedFirstInitialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, privateDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, initMarkerName), nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyFileName), []byte("partial"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	vault, err := OpenVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	vault.Close()
}

func TestVaultRejectsIncompleteState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, privateDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyFileName), []byte("partial"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(dir); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete-state failure, got %v", err)
	}
}

func TestVaultRejectsSymlinkedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires additional Windows privileges")
	}
	vault, dir := newVaultForTest(t)
	vault.Close()
	path := filepath.Join(dir, vaultFileName)
	target := filepath.Join(dir, "vault-target")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(dir); err == nil || !strings.Contains(err.Error(), "not regular") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}
