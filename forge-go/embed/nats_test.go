package embed

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedNATS(t *testing.T) {
	en, err := StartEmbeddedNATS()
	require.NoError(t, err)
	require.NotNil(t, en)
	storeDir := en.storeDir
	t.Cleanup(en.Close)

	require.DirExists(t, storeDir)

	require.NotEmpty(t, en.Host())
	require.NotZero(t, en.Port())
	require.NotEmpty(t, en.Addr())
	require.True(t, strings.HasPrefix(en.ClientURL(), "nats://"))

	// Pub/sub round-trip
	nc, err := en.Client()
	require.NoError(t, err)
	defer nc.Close()

	sub, err := nc.SubscribeSync("test.subject")
	require.NoError(t, err)

	err = nc.Publish("test.subject", []byte("hello"))
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	msg, err := sub.NextMsg(2 * time.Second)
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), msg.Data)
}

func TestEmbeddedNATSCloseRemovesTemporaryStore(t *testing.T) {
	en, err := StartEmbeddedNATS()
	require.NoError(t, err)
	storeDir := en.storeDir
	require.DirExists(t, storeDir)

	en.Close()
	require.NoDirExists(t, storeDir)
}

func TestPersistentEmbeddedNATSRetainsJetStreamAcrossRestart(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "nats")

	first, err := startEmbeddedNATSAt("", storeDir, false, 5*time.Second)
	require.NoError(t, err)
	nc, err := first.Client()
	require.NoError(t, err)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "HISTORY", Subjects: []string{"history.messages"}})
	require.NoError(t, err)
	_, err = js.Publish("history.messages", []byte("survives-restart"))
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	nc.Close()
	first.Close()
	require.DirExists(t, storeDir)

	second, err := startEmbeddedNATSAt("", storeDir, false, 5*time.Second)
	require.NoError(t, err)
	t.Cleanup(second.Close)
	nc, err = second.Client()
	require.NoError(t, err)
	defer nc.Close()
	js, err = nc.JetStream()
	require.NoError(t, err)
	msg, err := js.GetLastMsg("HISTORY", "history.messages")
	require.NoError(t, err)
	require.Equal(t, []byte("survives-restart"), msg.Data)
}

func TestPersistentEmbeddedNATSStartupFailurePreservesStore(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "nats")
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	marker := filepath.Join(storeDir, "existing-data")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o600))

	_, err := startEmbeddedNATSAt("invalid-address", storeDir, false, 500*time.Millisecond)
	require.Error(t, err)
	require.FileExists(t, marker)
}

func TestEmbeddedNATSAt_ExplicitAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	en, err := StartEmbeddedNATSAt(addr)
	require.NoError(t, err)
	require.NotNil(t, en)
	defer en.Close()

	require.Equal(t, addr, en.Addr())
}

func TestEmbeddedNATSAt_FailsWhenOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	_, err = StartEmbeddedNATSAt(ln.Addr().String())
	require.Error(t, err)
}

func TestEmbeddedNATS_JetStreamEnabled(t *testing.T) {
	en, err := StartEmbeddedNATS()
	require.NoError(t, err)
	defer en.Close()

	nc, err := en.Client()
	require.NoError(t, err)
	defer nc.Close()

	js, err := nc.JetStream()
	require.NoError(t, err)

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "TEST",
		Subjects: []string{"test.>"},
	})
	require.NoError(t, err)

	_, err = js.Publish("test.msg", []byte("jetstream-hello"))
	require.NoError(t, err)

	sub, err := js.PullSubscribe("test.msg", "test-consumer")
	require.NoError(t, err)

	msgs, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, []byte("jetstream-hello"), msgs[0].Data)
}
