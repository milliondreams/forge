package agent

import (
	"context"
	"strings"
	"testing"
)

func TestStartServerRejectsInvalidDependencyPrewarmMode(t *testing.T) {
	err := StartServer(context.Background(), &ServerConfig{ClientDependencyPrewarmMode: "eager"})
	if err == nil || !strings.Contains(err.Error(), "unsupported client dependency prewarm mode") {
		t.Fatalf("StartServer error = %v", err)
	}
}
