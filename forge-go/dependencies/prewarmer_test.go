package dependencies

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

func TestValidateMode(t *testing.T) {
	for _, mode := range []string{"", ModeOff, ModeGuild, "GUILD"} {
		if err := ValidateMode(mode); err != nil {
			t.Fatalf("ValidateMode(%q): %v", mode, err)
		}
	}
	if err := ValidateMode("eager"); err == nil {
		t.Fatal("expected unsupported mode to fail")
	}
}

func TestCoordinatorSingleFlightAndMemoryWarmReuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := testRegistry(t)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	c := testCoordinator(t, ctx, reg, 4, func(context.Context, string, []string, []string) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	})
	entry, _ := reg.Lookup("test.Agent")

	errs := make(chan error, 2)
	go func() { errs <- c.PrepareAgent(ctx, Metadata{AgentID: "a"}, entry, nil) }()
	<-started
	go func() { errs <- c.PrepareAgent(ctx, Metadata{AgentID: "b"}, entry, nil) }()
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1", got)
	}
	if err := c.PrepareAgent(ctx, Metadata{AgentID: "c"}, entry, nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("warm runner calls = %d, want 1", got)
	}
}

func TestCoordinatorUsesUnchangedNativeUVXPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv("FORGE_UV_PYTHON", "/bundled/python")
	t.Setenv("FORGE_PYTHON_PKG", "rusticai-forge==1")
	t.Setenv("FORGE_EXTRA_DEPS", "global-package")
	reg := testRegistry(t)
	var gotArgs []string
	c := testCoordinator(t, ctx, reg, 1, func(_ context.Context, _ string, args, _ []string) error {
		gotArgs = append([]string(nil), args...)
		return nil
	})
	entry, _ := reg.Lookup("test.Agent")
	extra := []string{"spec-package"}
	if err := c.PrepareAgent(ctx, Metadata{}, entry, extra); err != nil {
		t.Fatal(err)
	}
	command := registry.ResolveCommand(entry, extra)
	want := append([]string(nil), command[1:len(command)-3]...)
	want = append(want, "python", "-c", "import rustic_ai.forge.agent_runner")
	if strings.Join(gotArgs, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("prewarm args = %v, want %v", gotArgs, want)
	}
}

func TestWarmSystemUsesUnchangedNativeUVXPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv("FORGE_UV_PYTHON", "/bundled/python")
	t.Setenv("FORGE_PYTHON_PKG", "rusticai-forge==1")
	reg := testRegistry(t)
	gotArgs := make(chan []string, 1)
	c := testCoordinator(t, ctx, reg, 1, func(_ context.Context, _ string, args, _ []string) error {
		gotArgs <- append([]string(nil), args...)
		return nil
	})

	c.WarmSystem()
	entry := &registry.AgentRegistryEntry{Runtime: registry.RuntimeUVX}
	command := registry.ResolveCommand(entry, nil)
	want := append([]string(nil), command[1:len(command)-3]...)
	want = append(want, "python", "-c", "import rustic_ai.forge.agent_runner")
	select {
	case got := <-gotArgs:
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("system prewarm args = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("system prewarm did not run")
	}
}

func TestCoordinatorKeyIncludesModernIndexConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := testRegistry(t)
	c := testCoordinator(t, ctx, reg, 1, func(context.Context, string, []string, []string) error { return nil })
	requirements := []string{"rusticai-forge"}

	t.Setenv("UV_INDEX", "https://first.example/simple")
	first := c.key(requirements)
	t.Setenv("UV_INDEX", "https://second.example/simple")
	second := c.key(requirements)
	if first == second {
		t.Fatal("dependency key did not change with UV_INDEX")
	}
}

func TestCoordinatorLimitsConcurrencyToFour(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := testRegistry(t)
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	c := testCoordinator(t, ctx, reg, 4, func(context.Context, string, []string, []string) error {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		<-release
		active.Add(-1)
		return nil
	})
	entry, _ := reg.Lookup("test.Agent")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.PrepareAgent(ctx, Metadata{}, entry, []string{"unique-" + string(rune('a'+i))})
		}()
	}
	deadline := time.Now().Add(time.Second)
	for maximum.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if got := maximum.Load(); got != 4 {
		t.Fatalf("maximum concurrency = %d, want 4", got)
	}
}

func TestCoordinatorRetriesFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := testRegistry(t)
	var calls atomic.Int32
	c := testCoordinator(t, ctx, reg, 1, func(context.Context, string, []string, []string) error {
		if calls.Add(1) == 1 {
			return errors.New("temporary failure")
		}
		return nil
	})
	entry, _ := reg.Lookup("test.Agent")
	if err := c.PrepareAgent(ctx, Metadata{}, entry, nil); err == nil || !strings.Contains(err.Error(), "dependency_preparation_failed") {
		t.Fatalf("first error = %v", err)
	}
	if err := c.PrepareAgent(ctx, Metadata{}, entry, nil); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("runner calls = %d, want 2", got)
	}
}

func TestCoordinatorCancellationReleasesJoinedWaiters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reg := testRegistry(t)
	started := make(chan struct{})
	c := testCoordinator(t, ctx, reg, 1, func(ctx context.Context, _ string, _ []string, _ []string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	entry, _ := reg.Lookup("test.Agent")
	errCh := make(chan error, 2)
	go func() { errCh <- c.PrepareAgent(context.Background(), Metadata{AgentID: "a"}, entry, nil) }()
	<-started
	go func() { errCh <- c.PrepareAgent(context.Background(), Metadata{AgentID: "b"}, entry, nil) }()
	cancel()

	for range 2 {
		select {
		case err := <-errCh:
			if err == nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("joined preparation was not released on cancellation")
		}
	}
}

func TestPrepareGuildWaitsForEveryStaticAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := testRegistry(t)
	staticStarted := make(chan struct{})
	staticRelease := make(chan struct{})
	c := testCoordinator(t, ctx, reg, 2, func(_ context.Context, _ string, args, _ []string) error {
		if strings.Contains(strings.Join(args, " "), "static-package") {
			close(staticStarted)
			<-staticRelease
		}
		return nil
	})
	managerEntry, _ := reg.Lookup("manager.Agent")
	spec := &protocol.GuildSpec{Agents: []protocol.AgentSpec{{ID: "static", ClassName: "static.Agent"}}}
	done := make(chan error, 1)
	go func() {
		done <- c.PrepareGuild(ctx, Metadata{GuildID: "g", AgentID: "g#manager_agent"}, managerEntry, nil, spec)
	}()
	<-staticStarted
	select {
	case err := <-done:
		t.Fatalf("guild preparation completed before static agent: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(staticRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("guild preparation did not complete after static agent")
	}
}

func TestPreparePythonInstallsExactManagedVersionWithPersistentUVPaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var executable string
	var args, env []string
	c, err := NewCoordinator(Config{
		Context: ctx, Registry: testRegistry(t), UVXPath: "uvx", UVPath: "uv",
		Python: "3.13.13", UVVersion: "uvx 1", ForgeRevision: "revision",
		Run: func(_ context.Context, gotExecutable string, gotArgs, gotEnv []string) error {
			executable = gotExecutable
			args = append([]string(nil), gotArgs...)
			env = append([]string(nil), gotEnv...)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PreparePython(ctx); err != nil {
		t.Fatal(err)
	}
	if executable != "uv" || strings.Join(args, " ") != "python install 3.13.13" {
		t.Fatalf("command = %q %q", executable, args)
	}
	joined := strings.Join(env, "\n")
	for _, expected := range []string{
		"UV_CACHE_DIR=" + forgepath.Resolve("uv_cache"),
		"UV_PYTHON_INSTALL_DIR=" + forgepath.Resolve("python"),
		"UV_PYTHON_DOWNLOADS=automatic",
		"UV_MANAGED_PYTHON=1",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment does not contain %q: %s", expected, joined)
		}
	}
}

func TestPreparePythonRejectsSystemInterpreterAndRetriesFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := testRegistry(t)
	system, err := NewCoordinator(Config{
		Context: ctx, Registry: reg, UVXPath: "uvx", UVPath: "uv", Python: "/usr/bin/python3",
		UVVersion: "uvx 1", ForgeRevision: "revision",
		Run: func(context.Context, string, []string, []string) error {
			t.Fatal("system interpreter must not be executed for managed preparation")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := system.PreparePython(ctx); err == nil || !strings.Contains(err.Error(), "requires a managed Python version") {
		t.Fatalf("system Python error = %v", err)
	}

	attempts := 0
	retrying, err := NewCoordinator(Config{
		Context: ctx, Registry: reg, UVXPath: "uvx", UVPath: "uv", Python: "3.13.13",
		UVVersion: "uvx 1", ForgeRevision: "revision",
		Run: func(context.Context, string, []string, []string) error {
			attempts++
			if attempts == 1 {
				return errors.New("offline")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := retrying.PreparePython(ctx); err == nil {
		t.Fatal("first managed Python attempt unexpectedly succeeded")
	}
	if err := retrying.PreparePython(ctx); err != nil {
		t.Fatalf("explicit retry failed: %v", err)
	}
}

func TestSanitizeOutputRemovesURLCredentials(t *testing.T) {
	got := sanitizeOutput("download failed at https://user:secret@example.test/simple token-value", []string{"API_TOKEN=token-value"})
	if strings.Contains(got, "user:secret") || !strings.Contains(got, "https://***@example.test") {
		t.Fatalf("sanitized output = %q", got)
	}
	if strings.Contains(got, "token-value") {
		t.Fatalf("sanitized output retained token: %q", got)
	}
}

func testCoordinator(t *testing.T, ctx context.Context, reg *registry.Registry, workers int, run func(context.Context, string, []string, []string) error) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(Config{
		Context: ctx, Registry: reg, Workers: workers, UVXPath: "uvx", Python: "/python",
		UVVersion: "uvx 1", ForgeRevision: "revision", Run: run,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	path := t.TempDir() + "/registry.yaml"
	data := []byte(`entries:
  - id: test
    class_name: test.Agent
    runtime: uvx
    package: test-package
  - id: manager
    class_name: manager.Agent
    runtime: uvx
    package: manager-package
  - id: static
    class_name: static.Agent
    runtime: uvx
    package: static-package
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
