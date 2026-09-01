//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGracefulSignalTargetsLauncherBeforeForcedProcessGroupKill(t *testing.T) {
	cmd := exec.Command("sh", "-c", `trap 'exit 0' TERM; (trap '' TERM; while :; do sleep 1; done) & echo $! > child.pid; touch ready.txt; wait`)
	cmd.Dir = t.TempDir()
	configureCommandForProcessGroup(cmd, false)
	require.NoError(t, cmd.Start())

	childPIDPath := filepath.Join(cmd.Dir, "child.pid")
	readyPath := filepath.Join(cmd.Dir, "ready.txt")
	childPID := 0
	require.Eventually(t, func() bool {
		if _, err := os.Stat(readyPath); err != nil {
			return false
		}
		raw, err := os.ReadFile(childPIDPath)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && childPID > 0
	}, 5*time.Second, 25*time.Millisecond)

	pgid := processGroupID(cmd.Process.Pid)
	require.Positive(t, pgid)
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	signalUnixProcessTree(cmd.Process.Pid, pgid, []int{childPID}, syscall.SIGTERM)
	require.Eventually(t, func() bool {
		select {
		case <-waitDone:
			return true
		default:
			return false
		}
	}, 2*time.Second, 25*time.Millisecond)
	require.True(t, processExists(childPID), "graceful termination must be forwarded by the launcher, not broadcast to descendants")

	signalUnixProcessTree(cmd.Process.Pid, pgid, []int{childPID}, syscall.SIGKILL)
	require.Eventually(t, func() bool { return !processExists(childPID) }, 2*time.Second, 25*time.Millisecond)
}
