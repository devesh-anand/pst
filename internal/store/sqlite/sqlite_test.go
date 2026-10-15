package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devesh-anand/pst/internal/paste"
	"github.com/devesh-anand/pst/internal/paste/pastetest"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "pst.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMetaStore(t *testing.T) {
	pastetest.TestMetaStore(t, func(t *testing.T) paste.MetaStore { return openTestDB(t).Meta() })
}

func TestBlobStore(t *testing.T) {
	pastetest.TestBlobStore(t, func(t *testing.T) paste.BlobStore { return openTestDB(t).Blobs() })
}

func TestReopenKeepsDataAndSkipsMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pst.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Blobs().Put(ctx, "keep", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	db.Close()

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if _, err := db.Blobs().Get(ctx, "keep"); err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
}
