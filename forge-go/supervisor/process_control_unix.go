//go:build !windows

package supervisor

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v4/process"
)

const (
	processTerminationGracePeriod = 5 * time.Second
	processTerminationKillWait    = 2 * time.Second
	processTerminationPollPeriod  = 100 * time.Millisecond
	processExitConfirmations      = 3
)

func configureCommandForProcessGroup(cmd *exec.Cmd, detach bool) {
	// Always isolate the child into its own process group so launcher-style
	// commands like uvx can be terminated reliably as a unit during shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroupID(pid int) int {
	if pid <= 0 {
		return 0
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 0 || pgid == syscall.Getpgrp() {
		return 0
	}
	return pgid
}

func terminateProcessTree(pid, pgid int, detach bool) error {
	return terminateProcessTreeWithTimeout(
		pid,
		pgid,
		detach,
		processTerminationGracePeriod,
		processTerminationKillWait,
	)
}

func terminateProcessTreeWithTimeout(pid, pgid int, detach bool, gracePeriod, killWait time.Duration) error {
	if pgid <= 0 {
		pgid = processGroupID(pid)
	}

	descendants := []int(nil)
	// Every Unix child is launched in its own process group. Prefer that
	// authoritative ownership boundary and only walk descendants if the group
	// could not be recovered. Concurrent process-tree walks during context
	// cancellation and StopAll can otherwise stall shutdown on macOS.
	if pgid <= 0 && !detach && pid > 0 {
		descendants = descendantPIDs(pid)
	}

	signalUnixProcessTree(pid, pgid, descendants, syscall.SIGTERM)
	if waitForUnixProcessTreeExit(pid, pgid, descendants, gracePeriod) {
		return nil
	}

	signalUnixProcessTree(pid, pgid, descendants, syscall.SIGKILL)
	if waitForUnixProcessTreeExit(pid, pgid, descendants, killWait) {
		return nil
	}

	return fmt.Errorf("process tree for pid %d and process group %d remained alive after forced shutdown", pid, pgid)
}

func signalUnixProcessTree(pid, pgid int, descendants []int, signal syscall.Signal) {
	// Launcher-style runtimes such as uvx forward graceful termination to their
	// child. Broadcasting SIGTERM to the whole group would also deliver it
	// directly to that child, causing duplicate, potentially reentrant signal
	// handling. Give the owned launcher one graceful signal; forced shutdown
	// still targets the complete process group.
	if signal == syscall.SIGTERM && pid > 0 {
		_ = syscall.Kill(pid, signal)
	} else if pgid > 0 && pgid != syscall.Getpgrp() {
		slog.Info("signaling owned agent process group",
			"signal", signal,
			"target_pid", pid,
			"target_process_group_id", pgid,
			"forge_pid", os.Getpid(),
			"forge_process_group_id", syscall.Getpgrp(),
		)
		_ = syscall.Kill(-pgid, signal)
	} else if pid > 0 {
		_ = syscall.Kill(pid, signal)
	}

	// Descendant PIDs are only populated when a process group could not be
	// recovered. Preserve the same graceful-launcher/forced-tree distinction in
	// that fallback path.
	if signal != syscall.SIGTERM {
		for _, childPID := range descendants {
			if childPID > 0 {
				_ = syscall.Kill(childPID, signal)
			}
		}
	}
}

func waitForUnixProcessTreeExit(pid, pgid int, descendants []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	absentConfirmations := 0
	for {
		if !unixProcessTreeExists(pid, pgid, descendants) {
			absentConfirmations++
			if absentConfirmations >= processExitConfirmations {
				return true
			}
		} else {
			absentConfirmations = 0
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(processTerminationPollPeriod)
	}
}

func unixProcessTreeExists(pid, pgid int, descendants []int) bool {
	groupExists := unixProcessGroupExists(pgid)
	pidExists := processExists(pid)
	descendantExists := false
	for _, childPID := range descendants {
		if processExists(childPID) {
			descendantExists = true
			break
		}
	}
	if groupExists {
		return true
	}

	// Keep checking the concrete launcher and descendant PIDs even when a
	// process-group ID is available. Some launcher-style runtimes can make the
	// group probe transiently return ESRCH while the recorded processes are
	// still alive; treating that as terminal leaves their tree orphaned.
	if pidExists {
		return true
	}

	if descendantExists {
		return true
	}
	return false
}

func unixProcessGroupExists(pgid int) bool {
	if pgid <= 0 {
		return false
	}

	// The kernel is authoritative for whether the process group still exists.
	// Enumeration can briefly miss reparented launcher descendants on macOS;
	// treating that empty snapshot as terminal lets live agents escape cleanup.
	if err := syscall.Kill(-pgid, 0); err != nil && err != syscall.EPERM {
		return false
	}

	pids, err := gopsprocess.Pids()
	if err != nil {
		return true
	}

	foundGroupMember := false
	for _, candidatePID := range pids {
		candidateGroupID, err := syscall.Getpgid(int(candidatePID))
		if err != nil || candidateGroupID != pgid {
			continue
		}
		foundGroupMember = true
		candidate, err := gopsprocess.NewProcess(candidatePID)
		if err == nil {
			statuses, statusErr := candidate.Status()
			if statusErr == nil && slices.Contains(statuses, gopsprocess.Zombie) {
				continue
			}
		}
		return true
	}

	// If enumeration saw the group but every member was a zombie, it is no
	// longer a live workload. If enumeration saw nothing while kill(2) still
	// sees the group, retain ownership and retry rather than creating an orphan.
	return !foundGroupMember
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
