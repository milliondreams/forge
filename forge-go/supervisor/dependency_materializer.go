//go:build !windows

package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultDependencyRoot       = "/var/lib/agentos/dependencies"
	defaultDependencyIndexURL   = "https://pypi.org/simple"
	defaultDependencyMaxBytes   = int64(8 << 30)
	dependencyEnvironmentTarget = "/opt/agentos/runtime-environment"
)

// DependencyStatus is the additive AgentOS dependency portion of the status
// contract. Dependency failures do not make the Forge API itself unready.
type DependencyStatus struct {
	Phase                  string `json:"phase"`
	SystemEnvironmentReady bool   `json:"systemEnvironmentReady"`
	ActivePreparations     int    `json:"activePreparations"`
	LastError              string `json:"lastError,omitempty"`
}

// DependencyRequest contains the complete Python package request for one agent.
type DependencyRequest struct {
	Requirements []string
}

// DependencyEnvironment is an immutable environment lease. Release must be
// called only when the terminal agent lifecycle has finished.
type DependencyEnvironment struct {
	Path    string
	Key     string
	release func()
	once    sync.Once
}

func (e *DependencyEnvironment) Release() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		if e.release != nil {
			e.release()
		}
	})
}

type dependencyPreparation struct {
	done chan struct{}
	path string
	err  error
}

type dependencyReceipt struct {
	SchemaVersion    int      `json:"schema_version"`
	Key              string   `json:"key"`
	PythonABI        string   `json:"python_abi"`
	Architecture     string   `json:"architecture"`
	IndexURL         string   `json:"index_url"`
	ImageRevision    string   `json:"image_revision"`
	ForgeRevision    string   `json:"forge_revision"`
	SystemLockDigest string   `json:"system_lock_digest"`
	Requirements     []string `json:"requirements"`
	LockSHA256       string   `json:"lock_sha256"`
	Lock             string   `json:"lock"`
	CreatedAt        string   `json:"created_at"`
}

// DependencyMaterializerConfig is intentionally AgentOS-specific. Native
// supervisors never construct or consult it.
type DependencyMaterializerConfig struct {
	Root                  string
	UVPath                string
	PythonPath            string
	IndexURL              string
	InfrastructureDomains []string
	SystemLockPath        string
	SystemLockDigest      string
	SystemRequirements    []string
	ImageRevision         string
	ForgeRevision         string
	MaxBytes              int64
	BwrapPath             string
	NetProxyPath          string
	now                   func() time.Time
	run                   func(context.Context, string, []string, []string) error
	newRelay              func(string, string, []string) (dependencyRelay, error)
}

type dependencyRelay interface {
	SocketPath() string
	Close() error
}

// DependencyMaterializer owns the trusted, per-profile dependency cache.
type DependencyMaterializer struct {
	cfg DependencyMaterializerConfig

	storageMu    sync.Mutex
	mu           sync.Mutex
	inflight     map[string]*dependencyPreparation
	active       map[string]int
	status       DependencyStatus
	statusNotify func(DependencyStatus)
}

func NewDependencyMaterializer(cfg DependencyMaterializerConfig) (*DependencyMaterializer, error) {
	if cfg.Root == "" {
		cfg.Root = defaultDependencyRoot
	}
	if !filepath.IsAbs(cfg.Root) {
		return nil, errors.New("dependency root must be absolute")
	}
	cfg.Root = filepath.Clean(cfg.Root)
	if cfg.UVPath == "" {
		cfg.UVPath = "/usr/local/bin/uv"
	}
	if cfg.PythonPath == "" {
		cfg.PythonPath = "/usr/bin/python3.13"
	}
	if cfg.IndexURL == "" {
		cfg.IndexURL = defaultDependencyIndexURL
	}
	if cfg.IndexURL != defaultDependencyIndexURL {
		return nil, fmt.Errorf("unsupported AgentOS dependency index %q", cfg.IndexURL)
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultDependencyMaxBytes
	}
	if cfg.BwrapPath == "" {
		cfg.BwrapPath = "bwrap"
	}
	if cfg.NetProxyPath == "" {
		cfg.NetProxyPath = "/opt/agentos/bin/agentos-netproxy"
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.run == nil {
		cfg.run = runDependencyCommand
	}
	if cfg.newRelay == nil {
		cfg.newRelay = func(root, key string, domains []string) (dependencyRelay, error) {
			return NewAgentNetworkRelay(root, key, domains)
		}
	}
	if len(cfg.InfrastructureDomains) == 0 {
		return nil, errors.New("dependency infrastructure domains are required")
	}
	for name, revision := range map[string]string{"image": cfg.ImageRevision, "Forge": cfg.ForgeRevision} {
		if len(revision) != 40 || strings.Trim(revision, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("%s revision must be a full lowercase Git commit", name)
		}
	}
	for _, path := range []string{cfg.UVPath, cfg.PythonPath, cfg.SystemLockPath} {
		if path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("dependency prerequisite path %q must be absolute", path)
		}
	}
	lock, err := os.ReadFile(cfg.SystemLockPath)
	if err != nil {
		return nil, fmt.Errorf("read signed system dependency lock: %w", err)
	}
	lockSum := sha256.Sum256(lock)
	if !strings.EqualFold(cfg.SystemLockDigest, hex.EncodeToString(lockSum[:])) {
		return nil, errors.New("signed system dependency lock digest mismatch")
	}
	for _, dir := range []string{"cache", "environments", "receipts", "tmp", "locks", "access"} {
		if err := os.MkdirAll(filepath.Join(cfg.Root, dir), 0o700); err != nil {
			return nil, fmt.Errorf("create dependency %s directory: %w", dir, err)
		}
	}
	m := &DependencyMaterializer{
		cfg:      cfg,
		inflight: make(map[string]*dependencyPreparation),
		active:   make(map[string]int),
		status:   DependencyStatus{Phase: "idle"},
	}
	return m, nil
}

func NewDependencyMaterializerFromEnvironment() (*DependencyMaterializer, error) {
	domains := splitDependencyValues(os.Getenv("FORGE_AGENTOS_DEPENDENCY_DOMAINS"))
	systemRequirements := splitDependencyValues(os.Getenv("FORGE_AGENTOS_SYSTEM_REQUIREMENTS"))
	if len(systemRequirements) == 0 {
		forgePackage := strings.TrimSpace(os.Getenv("FORGE_PYTHON_PKG"))
		if forgePackage == "" {
			forgePackage = "rusticai-forge"
		}
		systemRequirements = []string{forgePackage, "rusticai-core"}
	}
	return NewDependencyMaterializer(DependencyMaterializerConfig{
		Root:                  strings.TrimSpace(os.Getenv("FORGE_AGENTOS_DEPENDENCY_ROOT")),
		UVPath:                strings.TrimSpace(os.Getenv("FORGE_AGENTOS_UV_PATH")),
		PythonPath:            strings.TrimSpace(os.Getenv("FORGE_AGENTOS_PYTHON_PATH")),
		IndexURL:              strings.TrimSpace(os.Getenv("FORGE_AGENTOS_DEPENDENCY_INDEX")),
		InfrastructureDomains: domains,
		SystemLockPath:        strings.TrimSpace(os.Getenv("FORGE_AGENTOS_SYSTEM_LOCK")),
		SystemLockDigest:      strings.TrimSpace(os.Getenv("FORGE_AGENTOS_SYSTEM_LOCK_SHA256")),
		SystemRequirements:    systemRequirements,
		ImageRevision:         strings.TrimSpace(os.Getenv("FORGE_AGENTOS_IMAGE_REVISION")),
		ForgeRevision:         strings.TrimSpace(os.Getenv("FORGE_AGENTOS_FORGE_REVISION")),
	})
}

func (m *DependencyMaterializer) SetStatusNotify(notify func(DependencyStatus)) {
	m.mu.Lock()
	m.statusNotify = notify
	status := m.status
	m.mu.Unlock()
	if notify != nil {
		notify(status)
	}
}

func (m *DependencyMaterializer) Status() DependencyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *DependencyMaterializer) WarmSystem(ctx context.Context) {
	request := DependencyRequest{Requirements: append([]string(nil), m.cfg.SystemRequirements...)}
	env, err := m.Prepare(ctx, request)
	if env != nil {
		env.Release()
	}
	m.mu.Lock()
	m.status.SystemEnvironmentReady = err == nil
	if err != nil {
		m.status.LastError = err.Error()
	}
	status, notify := m.status, m.statusNotify
	m.mu.Unlock()
	if notify != nil {
		notify(status)
	}
}

func (m *DependencyMaterializer) Prepare(ctx context.Context, request DependencyRequest) (*DependencyEnvironment, error) {
	requirements, err := normalizeDependencyRequirements(request.Requirements)
	if err != nil {
		return nil, dependencyMaterializationError(err)
	}
	key := m.environmentKey(requirements)

	m.mu.Lock()
	if preparation := m.inflight[key]; preparation != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, dependencyMaterializationError(ctx.Err())
		case <-preparation.done:
			if preparation.err != nil {
				return nil, dependencyMaterializationError(preparation.err)
			}
			return m.acquire(key, preparation.path), nil
		}
	}
	preparation := &dependencyPreparation{done: make(chan struct{})}
	m.inflight[key] = preparation
	m.status.Phase = "preparing"
	m.status.ActivePreparations++
	m.status.LastError = ""
	status, notify := m.status, m.statusNotify
	m.mu.Unlock()
	if notify != nil {
		notify(status)
	}

	path, prepareErr := m.prepare(ctx, key, requirements)
	m.mu.Lock()
	preparation.path = path
	preparation.err = prepareErr
	delete(m.inflight, key)
	m.status.ActivePreparations--
	if m.status.ActivePreparations == 0 {
		m.status.Phase = "idle"
	}
	if prepareErr != nil {
		m.status.LastError = prepareErr.Error()
	}
	status, notify = m.status, m.statusNotify
	close(preparation.done)
	m.mu.Unlock()
	if notify != nil {
		notify(status)
	}
	if prepareErr != nil {
		return nil, dependencyMaterializationError(prepareErr)
	}
	return m.acquire(key, path), nil
}

// ClearInactive removes cached artifacts, receipts, and environments that are
// not leased by a running agent. Active environments are never removed.
func (m *DependencyMaterializer) ClearInactive() error {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	m.mu.Lock()
	active := make(map[string]struct{}, len(m.active))
	for key := range m.active {
		active[key] = struct{}{}
	}
	m.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(m.cfg.Root, "environments"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, leased := active[entry.Name()]; leased {
			continue
		}
		if err := os.RemoveAll(filepath.Join(m.cfg.Root, "environments", entry.Name())); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(m.cfg.Root, "receipts", entry.Name()+".json"))
		_ = os.Remove(filepath.Join(m.cfg.Root, "access", entry.Name()))
	}
	if err := os.RemoveAll(filepath.Join(m.cfg.Root, "cache")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.cfg.Root, "cache"), 0o700); err != nil {
		return err
	}
	m.mu.Lock()
	m.status.SystemEnvironmentReady = false
	m.status.LastError = ""
	status, notify := m.status, m.statusNotify
	m.mu.Unlock()
	if notify != nil {
		notify(status)
	}
	return nil
}

func (m *DependencyMaterializer) acquire(key, path string) *DependencyEnvironment {
	m.mu.Lock()
	m.active[key]++
	m.mu.Unlock()
	_ = os.WriteFile(filepath.Join(m.cfg.Root, "access", key), []byte(m.cfg.now().UTC().Format(time.RFC3339Nano)), 0o600)
	return &DependencyEnvironment{Path: path, Key: key, release: func() {
		m.mu.Lock()
		if m.active[key] <= 1 {
			delete(m.active, key)
		} else {
			m.active[key]--
		}
		m.mu.Unlock()
	}}
}

func (m *DependencyMaterializer) prepare(ctx context.Context, key string, requirements []string) (string, error) {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	environment := filepath.Join(m.cfg.Root, "environments", key)
	receiptPath := filepath.Join(m.cfg.Root, "receipts", key+".json")
	if validEnvironment(environment, receiptPath, key) {
		return environment, nil
	}
	if _, err := os.Lstat(environment); err == nil {
		if err := os.RemoveAll(environment); err != nil {
			return "", fmt.Errorf("remove incomplete dependency environment: %w", err)
		}
	}
	if err := m.evictFor(m.cfg.MaxBytes / 20); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Join(m.cfg.Root, "tmp"), key+"-")
	if err != nil {
		return "", fmt.Errorf("create dependency staging directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	lockContent, err := m.resolveLock(ctx, key, tmp, requirements, receiptPath)
	if err != nil {
		return "", err
	}
	stagedEnvironment := filepath.Join(tmp, "environment")
	if err := m.runUV(ctx, key, []string{"venv", "--python", m.cfg.PythonPath, stagedEnvironment}); err != nil {
		return "", fmt.Errorf("create Python environment: %w", err)
	}
	lockPath := filepath.Join(tmp, "requirements.lock")
	if err := os.WriteFile(lockPath, lockContent, 0o600); err != nil {
		return "", fmt.Errorf("write dependency lock: %w", err)
	}
	installArgs := []string{
		"pip", "install", "--python", filepath.Join(stagedEnvironment, "bin", "python"),
		"--require-hashes", "--only-binary", ":all:",
		"--index-url", m.cfg.IndexURL, "--cache-dir", filepath.Join(m.cfg.Root, "cache"),
		"--requirement", lockPath,
	}
	if err := m.runUV(ctx, key, installArgs); err != nil {
		return "", fmt.Errorf("install locked dependency environment: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagedEnvironment, ".agentos-complete"), []byte(key+"\n"), 0o444); err != nil {
		return "", fmt.Errorf("mark dependency environment complete: %w", err)
	}
	if err := m.evictFor(0); err != nil {
		return "", err
	}
	if err := os.Rename(stagedEnvironment, environment); err != nil {
		if validEnvironment(environment, receiptPath, key) {
			return environment, nil
		}
		return "", fmt.Errorf("publish dependency environment: %w", err)
	}
	return environment, nil
}

func (m *DependencyMaterializer) resolveLock(ctx context.Context, key, tmp string, requirements []string, receiptPath string) ([]byte, error) {
	if receipt, err := readDependencyReceipt(receiptPath, key); err == nil {
		return []byte(receipt.Lock), nil
	}
	if sameRequirements(requirements, m.cfg.SystemRequirements) {
		lock, err := os.ReadFile(m.cfg.SystemLockPath)
		if err != nil {
			return nil, err
		}
		if err := m.writeReceipt(receiptPath, key, requirements, lock); err != nil {
			return nil, err
		}
		return lock, nil
	}
	input := filepath.Join(tmp, "requirements.in")
	lockPath := filepath.Join(tmp, "requirements.lock")
	if err := os.WriteFile(input, []byte(strings.Join(requirements, "\n")+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write dependency request: %w", err)
	}
	args := []string{
		"pip", "compile", "--python", m.cfg.PythonPath, "--generate-hashes",
		"--only-binary", ":all:", "--index-url", m.cfg.IndexURL,
		"--cache-dir", filepath.Join(m.cfg.Root, "cache"), "--output-file", lockPath, input,
	}
	if err := m.runUV(ctx, key, args); err != nil {
		return nil, fmt.Errorf("resolve binary-only dependency lock: %w", err)
	}
	lock, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read resolved dependency lock: %w", err)
	}
	if !bytesContainHashes(lock) {
		return nil, errors.New("resolved dependency lock contains no artifact hashes")
	}
	if err := m.writeReceipt(receiptPath, key, requirements, lock); err != nil {
		return nil, err
	}
	return lock, nil
}

func (m *DependencyMaterializer) writeReceipt(path, key string, requirements []string, lock []byte) error {
	sum := sha256.Sum256(lock)
	receipt := dependencyReceipt{
		SchemaVersion:    1,
		Key:              key,
		PythonABI:        "cp313",
		Architecture:     runtime.GOARCH,
		IndexURL:         m.cfg.IndexURL,
		ImageRevision:    m.cfg.ImageRevision,
		ForgeRevision:    m.cfg.ForgeRevision,
		SystemLockDigest: m.cfg.SystemLockDigest,
		Requirements:     requirements,
		LockSHA256:       hex.EncodeToString(sum[:]),
		Lock:             string(lock),
		CreatedAt:        m.cfg.now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write dependency receipt: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish dependency receipt: %w", err)
	}
	return nil
}

func (m *DependencyMaterializer) runUV(ctx context.Context, key string, uvArgs []string) error {
	relay, err := m.cfg.newRelay(filepath.Join(m.cfg.Root, "locks", "network"), "dependency-"+key, m.cfg.InfrastructureDomains)
	if err != nil {
		return err
	}
	defer relay.Close()
	socketDir := filepath.Dir(relay.SocketPath())
	command := append([]string{
		"--unshare-all", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
		"--tmpfs", "/tmp", "--tmpfs", "/home", "--die-with-parent", "--new-session",
		"--bind", m.cfg.Root, m.cfg.Root,
		"--ro-bind", socketDir, socketDir,
		"--", m.cfg.NetProxyPath, "--socket", relay.SocketPath(), "--listen", "127.0.0.1:18080", "--",
		m.cfg.UVPath,
	}, uvArgs...)
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/forge", "TMPDIR=/tmp", "LANG=C.UTF-8",
		"UV_NO_BINARY=false", "UV_PYTHON_DOWNLOADS=never", "UV_HTTP_TIMEOUT=30",
		"UV_CONCURRENT_DOWNLOADS=4",
		"HTTP_PROXY=http://127.0.0.1:18080", "HTTPS_PROXY=http://127.0.0.1:18080",
		"http_proxy=http://127.0.0.1:18080", "https_proxy=http://127.0.0.1:18080",
		"NO_PROXY=", "no_proxy=",
	}
	return m.cfg.run(ctx, m.cfg.BwrapPath, command, env)
}

func runDependencyCommand(ctx context.Context, executable string, args, env []string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(executable), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *DependencyMaterializer) environmentKey(requirements []string) string {
	canonical := strings.Join([]string{
		"schema=1", "python=cp313", "architecture=" + runtime.GOARCH,
		"index=" + m.cfg.IndexURL, "system_lock=" + strings.ToLower(m.cfg.SystemLockDigest),
		"image=" + m.cfg.ImageRevision,
		"forge=" + m.cfg.ForgeRevision, "requirements=" + strings.Join(requirements, "\n"),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func normalizeDependencyRequirements(input []string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, requirement := range input {
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			continue
		}
		if strings.ContainsAny(requirement, "\r\n\x00") || strings.HasPrefix(requirement, "-") {
			return nil, fmt.Errorf("invalid Python requirement %q", requirement)
		}
		lower := strings.ToLower(requirement)
		if filepath.IsAbs(requirement) || strings.HasPrefix(requirement, ".") ||
			strings.HasPrefix(lower, "file:") || strings.Contains(lower, "@ file:") {
			return nil, fmt.Errorf("local Python dependency %q is not permitted in AgentOS", requirement)
		}
		seen[requirement] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, errors.New("Python dependency request is empty")
	}
	out := make([]string, 0, len(seen))
	for requirement := range seen {
		out = append(out, requirement)
	}
	sort.Strings(out)
	return out, nil
}

func splitDependencyValues(value string) []string {
	values, _ := normalizeDependencyRequirements(strings.Split(value, ","))
	return values
}

func sameRequirements(left, right []string) bool {
	a, errA := normalizeDependencyRequirements(left)
	b, errB := normalizeDependencyRequirements(right)
	return errA == nil && errB == nil && strings.Join(a, "\n") == strings.Join(b, "\n")
}

func bytesContainHashes(lock []byte) bool {
	return strings.Contains(string(lock), "--hash=sha256:")
}

func validEnvironment(environment, receiptPath, key string) bool {
	marker, markerErr := os.ReadFile(filepath.Join(environment, ".agentos-complete"))
	python, pythonErr := os.Stat(filepath.Join(environment, "bin", "python"))
	_, receiptErr := readDependencyReceipt(receiptPath, key)
	return markerErr == nil && strings.TrimSpace(string(marker)) == key &&
		pythonErr == nil && python.Mode().IsRegular() && python.Mode().Perm()&0o111 != 0 && receiptErr == nil
}

func readDependencyReceipt(path, key string) (dependencyReceipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dependencyReceipt{}, err
	}
	var receipt dependencyReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return dependencyReceipt{}, err
	}
	if receipt.SchemaVersion != 1 || receipt.Key != key || receipt.Lock == "" {
		return dependencyReceipt{}, errors.New("invalid dependency receipt")
	}
	sum := sha256.Sum256([]byte(receipt.Lock))
	if !strings.EqualFold(receipt.LockSHA256, hex.EncodeToString(sum[:])) || !bytesContainHashes([]byte(receipt.Lock)) {
		return dependencyReceipt{}, errors.New("dependency receipt lock verification failed")
	}
	return receipt, nil
}

type dependencyEnvironmentUsage struct {
	key      string
	path     string
	accessed time.Time
	bytes    int64
}

func (m *DependencyMaterializer) evictFor(required int64) error {
	usage, total, err := m.measureUsage()
	if err != nil {
		return err
	}
	if total+required <= m.cfg.MaxBytes {
		return nil
	}
	sort.Slice(usage, func(i, j int) bool { return usage[i].accessed.Before(usage[j].accessed) })
	for _, candidate := range usage {
		m.mu.Lock()
		active := m.active[candidate.key] > 0
		m.mu.Unlock()
		if active {
			continue
		}
		if err := os.RemoveAll(candidate.path); err != nil {
			return fmt.Errorf("evict dependency environment %s: %w", candidate.key, err)
		}
		_ = os.Remove(filepath.Join(m.cfg.Root, "receipts", candidate.key+".json"))
		_ = os.Remove(filepath.Join(m.cfg.Root, "access", candidate.key))
		total -= candidate.bytes
		if total+required <= m.cfg.MaxBytes {
			return nil
		}
	}
	cache := filepath.Join(m.cfg.Root, "cache")
	if err := os.RemoveAll(cache); err != nil {
		return fmt.Errorf("clear dependency download cache: %w", err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return fmt.Errorf("recreate dependency download cache: %w", err)
	}
	_, total, err = m.measureUsage()
	if err != nil {
		return err
	}
	if total+required <= m.cfg.MaxBytes {
		return nil
	}
	return fmt.Errorf("dependency storage limit exceeded: need %d bytes with %d of %d bytes used", required, total, m.cfg.MaxBytes)
}

func (m *DependencyMaterializer) measureUsage() ([]dependencyEnvironmentUsage, int64, error) {
	root := filepath.Join(m.cfg.Root, "environments")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, 0, err
	}
	var usage []dependencyEnvironmentUsage
	total, err := directorySize(m.cfg.Root)
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		var size int64
		err := filepath.WalkDir(path, func(_ string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !item.Type().IsRegular() {
				return nil
			}
			info, infoErr := item.Info()
			if infoErr == nil {
				size += info.Size()
			}
			return infoErr
		})
		if err != nil {
			return nil, 0, err
		}
		accessed := time.Time{}
		if info, statErr := os.Stat(filepath.Join(m.cfg.Root, "access", entry.Name())); statErr == nil {
			accessed = info.ModTime()
		}
		usage = append(usage, dependencyEnvironmentUsage{key: entry.Name(), path: path, accessed: accessed, bytes: size})
	}
	return usage, total, nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !item.Type().IsRegular() {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func dependencyMaterializationError(err error) error {
	return fmt.Errorf("dependency_materialization_failed: %w", err)
}
