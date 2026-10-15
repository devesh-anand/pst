// Package pastetest provides a conformance suite for paste.MetaStore and
// paste.BlobStore implementations. A new backend (Postgres, S3, ...) should
// pass these before it is wired into main.
package pastetest

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devesh-anand/pst/internal/paste"
)

func newPaste(id string, expiresAt *time.Time) *paste.Paste {
	return &paste.Paste{
		ID:              id,
		CreatedAt:       time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt:       expiresAt,
		BurnAfterRead:   true,
		DeleteTokenHash: []byte("0123456789abcdef0123456789abcdef"),
		Size:            42,
	}
}

func ptr(t time.Time) *time.Time { return &t }

// TestMetaStore runs the MetaStore conformance suite. newStore must return an
// empty store; it is called once per subtest.
func TestMetaStore(t *testing.T, newStore func(t *testing.T) paste.MetaStore) {
	ctx := context.Background()

	t.Run("create and get round-trip", func(t *testing.T) {
		s := newStore(t)
		want := newPaste("abc123", ptr(time.Unix(1_700_000_600, 0).UTC()))
		if err := s.Create(ctx, want); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Get(ctx, "abc123")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.ID != want.ID || !got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(*want.ExpiresAt) ||
			got.BurnAfterRead != want.BurnAfterRead || !bytes.Equal(got.DeleteTokenHash, want.DeleteTokenHash) ||
			got.Size != want.Size {
			t.Fatalf("Get = %+v, want %+v", got, want)
		}
	})

	t.Run("nil expiry round-trips", func(t *testing.T) {
		s := newStore(t)
		if err := s.Create(ctx, newPaste("forever", nil)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Get(ctx, "forever")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.ExpiresAt != nil {
			t.Fatalf("ExpiresAt = %v, want nil", got.ExpiresAt)
		}
	})

	t.Run("duplicate id returns ErrExists", func(t *testing.T) {
		s := newStore(t)
		if err := s.Create(ctx, newPaste("dup", nil)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Create(ctx, newPaste("dup", nil)); !errors.Is(err, paste.ErrExists) {
			t.Fatalf("second Create err = %v, want ErrExists", err)
		}
	})

	t.Run("missing id returns ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Get(ctx, "nope"); !errors.Is(err, paste.ErrNotFound) {
			t.Fatalf("Get err = %v, want ErrNotFound", err)
		}
		if _, err := s.Take(ctx, "nope"); !errors.Is(err, paste.ErrNotFound) {
			t.Fatalf("Take err = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "nope"); !errors.Is(err, paste.ErrNotFound) {
			t.Fatalf("Delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete removes paste", func(t *testing.T) {
		s := newStore(t)
		if err := s.Create(ctx, newPaste("gone", nil)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Delete(ctx, "gone"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, "gone"); !errors.Is(err, paste.ErrNotFound) {
			t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("take has exactly one winner", func(t *testing.T) {
		s := newStore(t)
		if err := s.Create(ctx, newPaste("burn", nil)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := s.Take(ctx, "burn")
				switch {
				case err == nil && p.ID == "burn":
					wins.Add(1)
				case errors.Is(err, paste.ErrNotFound):
				default:
					t.Errorf("Take: %v", err)
				}
			}()
		}
		wg.Wait()
		if n := wins.Load(); n != 1 {
			t.Fatalf("Take winners = %d, want 1", n)
		}
	})

	t.Run("delete expired removes only expired", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_001_000, 0).UTC()
		for _, p := range []*paste.Paste{
			newPaste("old1", ptr(now.Add(-time.Hour))),
			newPaste("old2", ptr(now)),
			newPaste("fresh", ptr(now.Add(time.Hour))),
			newPaste("never", nil),
		} {
			if err := s.Create(ctx, p); err != nil {
				t.Fatalf("Create %s: %v", p.ID, err)
			}
		}
		ids, err := s.DeleteExpired(ctx, now, 1)
		if err != nil || len(ids) != 1 {
			t.Fatalf("DeleteExpired limit 1 = %v, %v; want 1 id", ids, err)
		}
		more, err := s.DeleteExpired(ctx, now, 100)
		if err != nil || len(more) != 1 {
			t.Fatalf("DeleteExpired = %v, %v; want 1 id", more, err)
		}
		gone := map[string]bool{ids[0]: true, more[0]: true}
		if !gone["old1"] || !gone["old2"] {
			t.Fatalf("deleted %v, want old1 and old2", gone)
		}
		for _, id := range []string{"fresh", "never"} {
			if _, err := s.Get(ctx, id); err != nil {
				t.Fatalf("Get %s after sweep: %v", id, err)
			}
		}
	})
}

// TestBlobStore runs the BlobStore conformance suite.
func TestBlobStore(t *testing.T, newStore func(t *testing.T) paste.BlobStore) {
	ctx := context.Background()

	t.Run("put get delete", func(t *testing.T) {
		s := newStore(t)
		data := []byte("ciphertext \x00\xff bytes")
		if err := s.Put(ctx, "blob1", data); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := s.Get(ctx, "blob1")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Get = %q, %v; want %q", got, err, data)
		}
		if err := s.Delete(ctx, "blob1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, "blob1"); !errors.Is(err, paste.ErrNotFound) {
			t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete missing is not an error", func(t *testing.T) {
		s := newStore(t)
		if err := s.Delete(ctx, "missing"); err != nil {
			t.Fatalf("Delete missing: %v", err)
		}
	})
}
