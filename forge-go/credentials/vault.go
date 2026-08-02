package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	vaultMagic      = "RUSTIC_AGENTOS_VAULT"
	vaultVersion    = 1
	vaultAlgorithm  = "AES-256-GCM-RANDOM-NONCE"
	keyFileName     = "master.key"
	vaultFileName   = "vault.bin"
	initMarkerName  = ".initializing"
	maxVaultBytes   = 16 << 20
	masterKeyBytes  = 32
	vaultIDBytes    = 16
	privateDirMode  = 0o700
	privateFileMode = 0o600
)

type keyDocument struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	VaultID string `json:"vaultId"`
	Key     string `json:"key"`
}

type vaultDocument struct {
	Magic      string `json:"magic"`
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	VaultID    string `json:"vaultId"`
	Ciphertext string `json:"ciphertext"`
}

type vaultPayload struct {
	Entries map[string]string `json:"entries"`
}

type Vault struct {
	mu      sync.RWMutex
	dir     string
	key     []byte
	vaultID string
	entries map[string]string
}

func OpenVault(dir string) (*Vault, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("AgentOS credential directory must be absolute")
	}
	if err := validateDirectory(dir); err != nil {
		return nil, err
	}
	if err := recoverInitialization(dir); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, keyFileName)
	vaultPath := filepath.Join(dir, vaultFileName)
	keyExists, err := regularFileExists(keyPath)
	if err != nil {
		return nil, err
	}
	vaultExists, err := regularFileExists(vaultPath)
	if err != nil {
		return nil, err
	}
	if !keyExists && !vaultExists {
		if err := initializeVault(dir); err != nil {
			return nil, err
		}
	} else if keyExists != vaultExists {
		return nil, errors.New("AgentOS credential vault is incomplete")
	}
	keyDoc, key, err := readKey(keyPath)
	if err != nil {
		return nil, err
	}
	entries, err := readVault(vaultPath, keyDoc.VaultID, key)
	if err != nil {
		clearBytes(key)
		return nil, err
	}
	return &Vault{dir: dir, key: key, vaultID: keyDoc.VaultID, entries: entries}, nil
}

func (v *Vault) Get(key string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	value, ok := v.entries[key]
	return value, ok
}

func (v *Vault) Put(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("credential key must not be empty")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	next := cloneEntries(v.entries)
	next[key] = value
	if err := v.persist(next); err != nil {
		return err
	}
	v.entries = next
	return nil
}

func (v *Vault) Delete(key string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.entries[key]; !ok {
		return false, nil
	}
	next := cloneEntries(v.entries)
	delete(next, key)
	if err := v.persist(next); err != nil {
		return false, err
	}
	v.entries = next
	return true, nil
}

func (v *Vault) List(prefix string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return sortedKeys(v.entries, prefix)
}

func (v *Vault) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clearBytes(v.key)
	v.key = nil
	v.entries = nil
}

func (v *Vault) persist(entries map[string]string) error {
	document, err := encryptVault(v.vaultID, v.key, entries)
	if err != nil {
		return err
	}
	data, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode AgentOS credential vault: %w", err)
	}
	if len(data) > maxVaultBytes {
		return fmt.Errorf("AgentOS credential vault exceeds %d bytes", maxVaultBytes)
	}
	if err := writeFileAtomic(v.dir, vaultFileName, append(data, '\n')); err != nil {
		return fmt.Errorf("persist AgentOS credential vault: %w", err)
	}
	return nil
}

func validateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect AgentOS credential directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("AgentOS credential path must be a directory, not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != privateDirMode {
		return fmt.Errorf("AgentOS credential directory permissions must be 0700, got %04o", info.Mode().Perm())
	}
	return nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("AgentOS credential file is not regular: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != privateFileMode {
		return false, fmt.Errorf("AgentOS credential file permissions must be 0600: %s", path)
	}
	return true, nil
}

func initializeVault(dir string) (retErr error) {
	markerPath := filepath.Join(dir, initMarkerName)
	marker, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return fmt.Errorf("create AgentOS vault initialization marker: %w", err)
	}
	if err := marker.Sync(); err != nil {
		marker.Close()
		return fmt.Errorf("sync AgentOS vault initialization marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("close AgentOS vault initialization marker: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = syncDirectory(dir)
		}
	}()
	key := make([]byte, masterKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("generate AgentOS vault key: %w", err)
	}
	defer clearBytes(key)
	vaultIDValue := make([]byte, vaultIDBytes)
	if _, err := io.ReadFull(rand.Reader, vaultIDValue); err != nil {
		return fmt.Errorf("generate AgentOS vault ID: %w", err)
	}
	vaultID := base64.RawURLEncoding.EncodeToString(vaultIDValue)
	keyData, err := json.Marshal(keyDocument{Magic: vaultMagic, Version: vaultVersion, VaultID: vaultID, Key: base64.RawStdEncoding.EncodeToString(key)})
	if err != nil {
		return fmt.Errorf("encode AgentOS vault key: %w", err)
	}
	vaultDoc, err := encryptVault(vaultID, key, map[string]string{})
	if err != nil {
		return err
	}
	vaultData, err := json.Marshal(vaultDoc)
	if err != nil {
		return fmt.Errorf("encode empty AgentOS vault: %w", err)
	}
	if err := writeFileAtomic(dir, keyFileName, append(keyData, '\n')); err != nil {
		return err
	}
	if err := writeFileAtomic(dir, vaultFileName, append(vaultData, '\n')); err != nil {
		return err
	}
	if err := os.Remove(markerPath); err != nil {
		return fmt.Errorf("complete AgentOS vault initialization: %w", err)
	}
	return syncDirectory(dir)
}

func recoverInitialization(dir string) error {
	markerPath := filepath.Join(dir, initMarkerName)
	if _, err := os.Lstat(markerPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect AgentOS vault initialization marker: %w", err)
	}
	for _, name := range []string{keyFileName, vaultFileName, initMarkerName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recover interrupted AgentOS vault initialization: %w", err)
		}
	}
	return syncDirectory(dir)
}

func readKey(path string) (keyDocument, []byte, error) {
	data, err := readLimitedFile(path)
	if err != nil {
		return keyDocument{}, nil, err
	}
	var document keyDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return document, nil, fmt.Errorf("decode AgentOS vault key: %w", err)
	}
	if document.Magic != vaultMagic || document.Version != vaultVersion || document.VaultID == "" {
		return document, nil, errors.New("unsupported or malformed AgentOS vault key")
	}
	key, err := base64.RawStdEncoding.DecodeString(document.Key)
	if err != nil || len(key) != masterKeyBytes {
		return document, nil, errors.New("AgentOS vault key is invalid")
	}
	return document, key, nil
}

func readVault(path, expectedVaultID string, key []byte) (map[string]string, error) {
	data, err := readLimitedFile(path)
	if err != nil {
		return nil, err
	}
	var document vaultDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode AgentOS credential vault: %w", err)
	}
	if document.Magic != vaultMagic || document.Version != vaultVersion || document.Algorithm != vaultAlgorithm || document.VaultID != expectedVaultID {
		return nil, errors.New("unsupported, malformed, or mismatched AgentOS credential vault")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(document.Ciphertext)
	if err != nil {
		return nil, errors.New("AgentOS credential vault ciphertext is invalid")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nil, ciphertext, vaultAAD(document.VaultID))
	if err != nil {
		return nil, errors.New("AgentOS credential vault authentication failed")
	}
	defer clearBytes(plaintext)
	if len(plaintext) > maxVaultBytes {
		return nil, errors.New("AgentOS credential vault plaintext is oversized")
	}
	var payload vaultPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("decode AgentOS credential payload: %w", err)
	}
	if payload.Entries == nil {
		return nil, errors.New("AgentOS credential vault entries are missing")
	}
	return payload.Entries, nil
}

func encryptVault(vaultID string, key []byte, entries map[string]string) (vaultDocument, error) {
	plaintext, err := json.Marshal(vaultPayload{Entries: entries})
	if err != nil {
		return vaultDocument{}, fmt.Errorf("encode AgentOS credential payload: %w", err)
	}
	defer clearBytes(plaintext)
	if len(plaintext) > maxVaultBytes {
		return vaultDocument{}, errors.New("AgentOS credential payload is oversized")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return vaultDocument{}, err
	}
	ciphertext := aead.Seal(nil, nil, plaintext, vaultAAD(vaultID))
	return vaultDocument{Magic: vaultMagic, Version: vaultVersion, Algorithm: vaultAlgorithm, VaultID: vaultID, Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext)}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AgentOS vault cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("initialize AgentOS vault AEAD: %w", err)
	}
	return aead, nil
}

func vaultAAD(vaultID string) []byte {
	return []byte(fmt.Sprintf("%s:%d:%s:%s", vaultMagic, vaultVersion, vaultAlgorithm, vaultID))
}

func readLimitedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open AgentOS credential file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxVaultBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read AgentOS credential file: %w", err)
	}
	if len(data) > maxVaultBytes {
		return nil, errors.New("AgentOS credential file is oversized")
	}
	return data, nil
}

func writeFileAtomic(dir, name string, data []byte) (retErr error) {
	randomSuffix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, randomSuffix); err != nil {
		return err
	}
	tempPath := filepath.Join(dir, "."+name+".tmp-"+base64.RawURLEncoding.EncodeToString(randomSuffix))
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		if retErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
