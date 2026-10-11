package store

import (
	"context"
	"errors"
	"testing"
)

func TestXORMStoreUnconfiguredOperationsFailClearly(t *testing.T) {
	ctx := context.Background()
	var nilStore *Store
	if nilStore.Pool() != nil {
		t.Fatal("nil Store should not expose a pool")
	}
	if err := (&Store{}).Migrate(ctx); !errors.Is(err, errXORMNotConfigured) {
		t.Fatalf("Migrate error = %v, want %v", err, errXORMNotConfigured)
	}

	var pool *xormPool
	if err := pool.Ping(ctx); !errors.Is(err, errXORMNotConfigured) {
		t.Fatalf("Ping error = %v, want %v", err, errXORMNotConfigured)
	}
	if _, err := pool.Query(ctx, "SELECT 1"); !errors.Is(err, errXORMNotConfigured) {
		t.Fatalf("Query error = %v, want %v", err, errXORMNotConfigured)
	}
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(new(int)); !errors.Is(err, errXORMNotConfigured) {
		t.Fatalf("QueryRow error = %v, want %v", err, errXORMNotConfigured)
	}
	if _, err := pool.Exec(ctx, "SELECT 1"); !errors.Is(err, errXORMNotConfigured) {
		t.Fatalf("Exec error = %v, want %v", err, errXORMNotConfigured)
	}
	if _, err := pool.Begin(ctx); !errors.Is(err, errXORMNotConfigured) {
		t.Fatalf("Begin error = %v, want %v", err, errXORMNotConfigured)
	}
}
