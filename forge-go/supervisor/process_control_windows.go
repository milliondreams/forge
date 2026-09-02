//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v4/process"
)

const (
	processTerminationGracePeriod = 5 * time.Second
	processTerminationKillWait    = 2 * time.Second
	processTerminationPollPeriod  = 100 * time.Millisecond
)

func configureCommandForProcessGroup(cmd *exec.Cmd, detach bool) {
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
		return
	}

	cmd.SysProcAttr = nil
}

func processGroupID(_ int) int {
	return 0
}

func terminateProcessTree(pid, _ int, detach bool) error {
	return terminateProcessTreeWithTimeout(
		pid,
		0,
		detach,
		processTerminationGracePeriod,
		processTerminationKillWait,
	)
}

func terminateProcessTreeWithTimeout(pid, _ int, detach bool, gracePeriod, killWait time.Duration) error {
	if !detach {
		return terminateAttachedProcessTreeWithTimeout(pid, gracePeriod, killWait)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}

	// Try a graceful interrupt first.
	_ = proc.Signal(os.Interrupt)

	if waitForWindowsPIDsExit([]int{pid}, gracePeriod) {
		return nil
	}

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	if waitForWindowsPIDsExit([]int{pid}, killWait) {
		return nil
	}
	return fmt.Errorf("process %d remained alive after forced shutdown", pid)
}

func terminateAttachedProcessTree(pid int) error {
	return terminateAttachedProcessTreeWithTimeout(
		pid,
		processTerminationGracePeriod,
		processTerminationKillWait,
	)
}

func terminateAttachedProcessTreeWithTimeout(pid int, gracePeriod, killWait time.Duration) error {
	descendants := descendantPIDs(pid)
	processes := append(append([]int{}, descendants...), pid)
	for _, childPID := range processes {
		if proc, err := os.FindProcess(childPID); err == nil {
			_ = proc.Signal(os.Interrupt)
		}
	}
	if waitForWindowsPIDsExit(processes, gracePeriod) {
		return nil
	}

	for _, childPID := range processes {
		alive, err := gopsprocess.PidExists(int32(childPID))
		if err == nil && alive {
			if childProc, findErr := os.FindProcess(childPID); findErr == nil {
				_ = childProc.Kill()
			}
		}
	}

	if waitForWindowsPIDsExit(processes, killWait) {
		return nil
	}
	return fmt.Errorf("attached process tree for pid %d remained alive after forced shutdown", pid)
}

func waitForWindowsPIDsExit(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive := false
		for _, pid := range pids {
			if processExists(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(processTerminationPollPeriod)
	}
}

func processExists(pid int) bool {
	exists, err := gopsprocess.PidExists(int32(pid))
	return err == nil && exists
}
