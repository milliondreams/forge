package secrets

import (
	"context"
	"sync"
	"testing"
	"time"
)

type countingProvider struct {
	mu     sync.Mutex
	values map[string]string
	reads  map[string]int
	delay  time.Duration
}

func (p *countingProvider) Resolve(_ context.Context, key string) (string, error) {
	time.Sleep(p.delay)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads[key]++
	value, ok := p.values[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func TestCachedProvider_CoalescesAndInvalidates(t *testing.T) {
	underlying := &countingProvider{values: map[string]string{"KEY": "one"}, reads: map[string]int{}, delay: 10 * time.Millisecond}
	provider := NewCachedProvider(underlying)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := provider.Resolve(context.Background(), "KEY")
			if err != nil || value != "one" {
				t.Errorf("Resolve() = %q, %v", value, err)
			}
		}()
	}
	wg.Wait()
	if reads := underlying.reads["KEY"]; reads != 1 {
		t.Fatalf("expected one underlying read, got %d", reads)
	}

	underlying.values["KEY"] = "two"
	provider.Invalidate("KEY")
	value, err := provider.Resolve(context.Background(), "KEY")
	if err != nil || value != "two" || underlying.reads["KEY"] != 2 {
		t.Fatalf("invalidation did not force a fresh read: value=%q err=%v reads=%d", value, err, underlying.reads["KEY"])
	}
}

func TestCachedProvider_DoesNotCacheMissingValues(t *testing.T) {
	underlying := &countingProvider{values: map[string]string{}, reads: map[string]int{}}
	provider := NewCachedProvider(underlying)
	for range 2 {
		if _, err := provider.Resolve(context.Background(), "MISSING"); err != ErrSecretNotFound {
			t.Fatalf("expected ErrSecretNotFound, got %v", err)
		}
	}
	if reads := underlying.reads["MISSING"]; reads != 2 {
		t.Fatalf("missing values must not be cached, got %d reads", reads)
	}
}
