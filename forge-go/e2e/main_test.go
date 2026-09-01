package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/registry"
)

var (
	e2eBaseDir  string
	e2eForgeBin string
)

func requireE2EForgeBin(t *testing.T) string {
	t.Helper()
	if e2eForgeBin == "" {
		t.Fatal("e2e forge binary path not initialized")
	}
	return e2eForgeBin
}

func TestMain(m *testing.M) {
	baseDir, err := os.MkdirTemp("", "forge-e2e-env-*")
	if err == nil {
		e2eBaseDir = baseDir
		homeDir := filepath.Join(baseDir, "home")
		xdgCache := filepath.Join(baseDir, "xdg-cache")
		xdgData := filepath.Join(baseDir, "xdg-data")
		uvCache := filepath.Join(baseDir, "uv-cache")
		tmpDir := filepath.Join(baseDir, "tmp")
		binDir := filepath.Join(baseDir, "bin")
		_ = os.MkdirAll(homeDir, 0o755)
		_ = os.MkdirAll(xdgCache, 0o755)
		_ = os.MkdirAll(xdgData, 0o755)
		_ = os.MkdirAll(uvCache, 0o755)
		_ = os.MkdirAll(tmpDir, 0o755)
		_ = os.MkdirAll(binDir, 0o755)

		_ = os.Setenv("HOME", homeDir)
		_ = os.Setenv("XDG_CACHE_HOME", xdgCache)
		_ = os.Setenv("XDG_DATA_HOME", xdgData)
		_ = os.Setenv("TMPDIR", tmpDir)
		_ = os.Setenv("FORGE_UV_CACHE_DIR", uvCache)
		_ = os.Setenv("UV_CACHE_DIR", uvCache)
		_ = os.Setenv("FORGE_LOCAL_USER_ID", "test-user")
		_ = os.Setenv("FORGE_LOCAL_USER_NAME", "Test User")
		_ = os.Setenv("FORGE_LOCAL_ORGANIZATION_ID", "test-org")
		_ = os.Setenv("FORGE_LOCAL_ORGANIZATION_NAME", "Test Organization")

		if _, lookErr := exec.LookPath("uvx"); lookErr != nil {
			if ensureErr := registry.EnsureUV(); ensureErr != nil {
				fmt.Fprintf(os.Stderr, "failed to ensure uv for e2e tests: %v\n", ensureErr)
				os.Exit(1)
			}
		}

		// Build once for the full e2e package to avoid repeated go-build temp churn.
		e2eForgeBin = filepath.Join(binDir, "forge_e2e")
		buildCmd := exec.Command("go", "build", "-o", e2eForgeBin, "../main.go")
		buildOut, buildErr := buildCmd.CombinedOutput()
		if buildErr != nil {
			fmt.Fprintf(os.Stderr, "failed to build e2e forge binary: %v\n%s\n", buildErr, string(buildOut))
			os.Exit(1)
		}

		// Pre-pull the default Docker agent image so individual tests don't time out on a cold pull.
		if out, err := exec.Command("docker", "pull", "ghcr.io/astral-sh/uv:python3.13-bookworm-slim").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "docker pre-pull (non-fatal): %v\n%s\n", err, out)
		}
	}
	code := m.Run()
	if e2eBaseDir != "" {
		_ = os.RemoveAll(e2eBaseDir)
	}
	os.Exit(code)
}
