//go:build !windows

package embed

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedNATSDoesNotOwnProcessSignals(t *testing.T) {
	if os.Getenv("FORGE_EMBEDDED_NATS_SIGNAL_HELPER") == "1" {
		en, err := StartEmbeddedNATS()
		if err != nil {
			os.Exit(2)
		}
		defer en.Close()
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)

		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			os.Exit(3)
		}
		select {
		case <-signals:
		case <-time.After(time.Second):
			os.Exit(4)
		}
		time.Sleep(250 * time.Millisecond)
		_, _ = os.Stdout.WriteString("survived SIGTERM\n")
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEmbeddedNATSDoesNotOwnProcessSignals$")
	cmd.Env = append(os.Environ(), "FORGE_EMBEDDED_NATS_SIGNAL_HELPER=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "survived SIGTERM")
}
