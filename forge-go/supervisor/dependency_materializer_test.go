//go:build !windows

package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeDependencyRequirementsDeterministic(t *testing.T) {
	got, err := normalizeDependencyRequirements([]string{" rusticai-core>=1,<2 ", "rusticai-forge", "rusticai-forge"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"rusticai-core>=1,<2", "rusticai-forge"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requirements = %v, want %v", got, want)
	}
	for _, invalid := range []string{"--index-url=https://evil.example", "/tmp/local.whl", "file:///tmp/local.whl", "pkg @ file:///tmp/local.whl"} {
		if _, err := normalizeDependencyRequirements([]string{invalid}); err == nil {
			t.Fatalf("accepted invalid requirement %q", invalid)
		}
	}
}

func TestDependencyEnvironmentKeyIncludesRevisionAndIsOrderIndependent(t *testing.T) {
	m := testDependencyMaterializer(t, nil)
	left, _ := normalizeDependencyRequirements([]string{"b", "a"})
	right, _ := normalizeDependencyRequirements([]string{"a", "b"})
	if m.environmentKey(left) != m.environmentKey(right) {
		t.Fatal("environment key depends on request ordering")
	}
	other := &DependencyMaterializer{cfg: m.cfg}
	other.cfg.ForgeRevision = strings.Repeat("c", 40)
	if m.environmentKey(left) == other.environmentKey(right) {
		t.Fatal("environment key ignores Forge revision")
	}
	other = &DependencyMaterializer{cfg: m.cfg}
	other.cfg.ImageRevision = strings.Repeat("d", 40)
	if m.environmentKey(left) == other.environmentKey(right) {
		t.Fatal("environment key ignores image revision")
	}
}

func TestDependencyMaterializerSingleFlightAndWarmOfflineReuse(t *testing.T) {
	var compileCalls atomic.Int32
	m := testDependencyMaterializer(t, func(_ context.Context, _ string, args, _ []string) error {
		if containsArg(args, "compile") {
			compileCalls.Add(1)
			output := argAfter(args, "--output-file")
			return os.WriteFile(output, []byte("example==1.0 \\"+"\n    --hash=sha256:"+strings.Repeat("a", 64)+"\n"), 0o600)
		}
		if containsArg(args, "venv") {
			environment := args[len(args)-1]
			if err := os.MkdirAll(filepath.Join(environment, "bin"), 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(environment, "bin", "python"), []byte("python"), 0o700)
		}
		return nil
	})

	request := DependencyRequest{Requirements: []string{"example"}}
	var wg sync.WaitGroup
	results := make(chan *DependencyEnvironment, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			environment, err := m.Prepare(context.Background(), request)
			if err != nil {
				t.Errorf("Prepare: %v", err)
				return
			}
			results <- environment
		}()
	}
	wg.Wait()
	close(results)
	var path string
	for environment := range results {
		if path == "" {
			path = environment.Path
		}
		if environment.Path != path {
			t.Fatalf("single-flight paths differ: %q and %q", path, environment.Path)
		}
		environment.Release()
	}
	if compileCalls.Load() != 1 {
		t.Fatalf("compile calls = %d, want 1", compileCalls.Load())
	}

	// A valid receipt and environment are usable without invoking uv again.
	m.cfg.run = func(context.Context, string, []string, []string) error {
		t.Fatal("warm environment attempted dependency acquisition")
		return nil
	}
	environment, err := m.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	environment.Release()
}

func TestDependencyMaterializerCancellationLeavesNoEnvironment(t *testing.T) {
	m := testDependencyMaterializer(t, func(ctx context.Context, _ string, args, _ []string) error {
		if containsArg(args, "compile") {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Prepare(ctx, DependencyRequest{Requirements: []string{"example"}})
	if err == nil || !strings.Contains(err.Error(), "dependency_materialization_failed") {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(m.cfg.Root, "environments"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("cancelled preparation published %d environments", len(entries))
	}
}

func TestDependencyMaterializerRejectsUnhashedResolution(t *testing.T) {
	m := testDependencyMaterializer(t, func(_ context.Context, _ string, args, _ []string) error {
		if containsArg(args, "compile") {
			return os.WriteFile(argAfter(args, "--output-file"), []byte("example==1.0\n"), 0o600)
		}
		return nil
	})
	_, err := m.Prepare(context.Background(), DependencyRequest{Requirements: []string{"example"}})
	if err == nil || !strings.Contains(err.Error(), "contains no artifact hashes") {
		t.Fatalf("unexpected unhashed resolution error: %v", err)
	}
}

func TestDependencyMaterializerColdOfflineFailureIsActionable(t *testing.T) {
	m := testDependencyMaterializer(t, func(_ context.Context, _ string, args, _ []string) error {
		if containsArg(args, "compile") {
			return errors.New("package index unavailable")
		}
		return nil
	})
	_, err := m.Prepare(context.Background(), DependencyRequest{Requirements: []string{"example"}})
	if err == nil || !strings.Contains(err.Error(), "dependency_materialization_failed") ||
		!strings.Contains(err.Error(), "package index unavailable") {
		t.Fatalf("unexpected cold-offline error: %v", err)
	}
	if !strings.Contains(m.Status().LastError, "package index unavailable") {
		t.Fatalf("status did not retain the dependency failure: %+v", m.Status())
	}
}

func TestDependencyMaterializerRecoversCorruptEnvironment(t *testing.T) {
	var venvCalls atomic.Int32
	m := testDependencyMaterializer(t, func(_ context.Context, _ string, args, _ []string) error {
		if containsArg(args, "compile") {
			return os.WriteFile(argAfter(args, "--output-file"), []byte("example==1.0 \\"+"\n    --hash=sha256:"+strings.Repeat("a", 64)+"\n"), 0o600)
		}
		if containsArg(args, "venv") {
			venvCalls.Add(1)
			environment := args[len(args)-1]
			if err := os.MkdirAll(filepath.Join(environment, "bin"), 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(environment, "bin", "python"), []byte("python"), 0o700)
		}
		return nil
	})
	request := DependencyRequest{Requirements: []string{"example"}}
	environment, err := m.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	path := environment.Path
	environment.Release()
	if err := os.Remove(filepath.Join(path, "bin", "python")); err != nil {
		t.Fatal(err)
	}
	environment, err = m.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	environment.Release()
	if venvCalls.Load() != 2 {
		t.Fatalf("venv calls = %d, want corruption rebuild", venvCalls.Load())
	}
}

func TestDependencyMaterializerUsesBinaryOnlyFetchSandbox(t *testing.T) {
	var checked bool
	m := testDependencyMaterializer(t, func(_ context.Context, executable string, args, env []string) error {
		if executable != "bwrap" || !containsArg(args, "--unshare-all") || !containsArg(args, "--only-binary") || containsArg(args, "--no-build") {
			t.Fatalf("dependency command escaped binary-only sandbox: %s %v", executable, args)
		}
		if !containsArg(env, "HTTPS_PROXY=http://127.0.0.1:18080") {
			t.Fatalf("dependency command lacks restricted proxy: %v", env)
		}
		if !containsArg(env, "UV_HTTP_TIMEOUT=30") {
			t.Fatalf("dependency command lacks bounded slow-network timeout: %v", env)
		}
		if !containsArg(env, "UV_CONCURRENT_DOWNLOADS=4") {
			t.Fatalf("dependency command does not bound proxy connection pressure: %v", env)
		}
		checked = true
		return errors.New("stop after policy assertion")
	})
	_, _ = m.Prepare(context.Background(), DependencyRequest{Requirements: []string{"example"}})
	if !checked {
		t.Fatal("dependency resolver was not invoked")
	}
}

func TestDependencyMaterializerDoesNotEvictActiveEnvironment(t *testing.T) {
	m := testDependencyMaterializer(t, nil)
	activePath := filepath.Join(m.cfg.Root, "environments", strings.Repeat("a", 64))
	inactivePath := filepath.Join(m.cfg.Root, "environments", strings.Repeat("b", 64))
	for _, path := range []string{activePath, inactivePath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "payload"), make([]byte, 128), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.active[filepath.Base(activePath)] = 1
	m.cfg.MaxBytes = 200
	if err := m.evictFor(1); err == nil {
		t.Fatal("expected storage error while active environment consumes the budget")
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active environment was evicted: %v", err)
	}
	if _, err := os.Stat(inactivePath); !os.IsNotExist(err) {
		t.Fatalf("inactive environment was not evicted: %v", err)
	}
}

func testDependencyMaterializer(t *testing.T, run func(context.Context, string, []string, []string) error) *DependencyMaterializer {
	t.Helper()
	root := t.TempDir()
	lock := []byte("rusticai-core==1.0 \\" + "\n    --hash=sha256:" + strings.Repeat("f", 64) + "\n")
	lockPath := filepath.Join(root, "system.lock")
	if err := os.WriteFile(lockPath, lock, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(lock)
	if run == nil {
		run = func(context.Context, string, []string, []string) error { return nil }
	}
	m, err := NewDependencyMaterializer(DependencyMaterializerConfig{
		Root: root, UVPath: "/usr/local/bin/uv", PythonPath: "/usr/bin/python3.13",
		IndexURL: defaultDependencyIndexURL, InfrastructureDomains: []string{"pypi.org", "files.pythonhosted.org"},
		SystemLockPath: lockPath, SystemLockDigest: hex.EncodeToString(sum[:]),
		SystemRequirements: []string{"rusticai-forge", "rusticai-core"},
		ImageRevision:      strings.Repeat("a", 40), ForgeRevision: strings.Repeat("b", 40),
		MaxBytes: 1 << 20, now: func() time.Time { return time.Unix(1_700_000_000, 0) }, run: run,
		newRelay: func(string, string, []string) (dependencyRelay, error) {
			return testDependencyRelay{path: filepath.Join(root, "locks", "proxy.sock")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type testDependencyRelay struct{ path string }

func (r testDependencyRelay) SocketPath() string { return r.path }
func (testDependencyRelay) Close() error         { return nil }

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func argAfter(args []string, value string) string {
	for index, arg := range args {
		if arg == value && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
