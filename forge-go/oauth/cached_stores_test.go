package oauth

import (
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type cacheCountingTokenStore struct {
	entry   *tokenEntry
	loads   int
	saves   int
	deletes int
}

func (s *cacheCountingTokenStore) Save(_, _ string, entry *tokenEntry) error {
	s.entry = cloneTokenEntry(entry)
	s.saves++
	return nil
}
func (s *cacheCountingTokenStore) Load(_, _ string) (*tokenEntry, bool, error) {
	s.loads++
	return cloneTokenEntry(s.entry), s.entry != nil, nil
}
func (s *cacheCountingTokenStore) Delete(_, _ string) bool {
	s.entry = nil
	s.deletes++
	return true
}

func TestCachedTokenStore_CachesRecordsAndInvalidates(t *testing.T) {
	underlying := &cacheCountingTokenStore{entry: &tokenEntry{token: &oauth2.Token{AccessToken: "one", Expiry: time.Now().Add(time.Hour)}}}
	store := NewCachedTokenStore(underlying)

	first, ok, err := store.Load("org", "provider")
	if err != nil || !ok || first.token.AccessToken != "one" {
		t.Fatal("failed to load token")
	}
	first.token.AccessToken = "mutated"
	second, _, _ := store.Load("org", "provider")
	if second.token.AccessToken != "one" || underlying.loads != 1 {
		t.Fatalf("cache must clone records and read once: token=%q loads=%d", second.token.AccessToken, underlying.loads)
	}

	store.Delete("org", "provider")
	if _, ok, _ := store.Load("org", "provider"); ok || underlying.loads != 2 {
		t.Fatalf("delete must invalidate cache: ok=%v loads=%d", ok, underlying.loads)
	}
}
