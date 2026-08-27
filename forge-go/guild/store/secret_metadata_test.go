package store_test

import (
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

func TestSecretMetadataPersistsNamesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	db, err := store.NewGormStore(store.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	index, ok := db.(secrets.MetadataIndex)
	if !ok {
		t.Fatal("gorm store does not implement secrets.MetadataIndex")
	}
	if err := index.Add("org", "OPENAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db, err = store.NewGormStore(store.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	index = db.(secrets.MetadataIndex)
	names, err := index.List("org")
	if err != nil || len(names) != 1 || names[0] != "OPENAI_API_KEY" {
		t.Fatalf("persisted metadata = %v, %v", names, err)
	}
}
