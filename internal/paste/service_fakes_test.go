package paste_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devesh-anand/pst/internal/paste"
)

// collidingMeta reports ErrExists for the first n creates, then delegates.
type collidingMeta struct {
	paste.MetaStore
	collisions int
	attempts   int
	deleted    []string
}

func (m *collidingMeta) Create(ctx context.Context, p *paste.Paste) error {
	m.attempts++
	if m.attempts <= m.collisions {
		return paste.ErrExists
	}
	return m.MetaStore.Create(ctx, p)
}

func (m *collidingMeta) Delete(ctx context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return m.MetaStore.Delete(ctx, id)
}

type failingBlobs struct{ paste.BlobStore }

func (failingBlobs) Put(context.Context, string, []byte) error { return errors.New("disk full") }

func newServiceWith(t *testing.T, wrap func(paste.MetaStore, paste.BlobStore) (paste.MetaStore, paste.BlobStore)) *paste.Service {
	t.Helper()
	db := openDB(t)
	meta, blobs := wrap(db.Meta(), db.Blobs())
	svc, err := paste.NewService(meta, blobs, paste.Options{
		IDLength: 10, MaxPasteBytes: 1024, Expiries: []time.Duration{time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestCreateRetriesOnIDCollision(t *testing.T) {
	var meta *collidingMeta
	svc := newServiceWith(t, func(m paste.MetaStore, b paste.BlobStore) (paste.MetaStore, paste.BlobStore) {
		meta = &collidingMeta{MetaStore: m, collisions: 3}
		return meta, b
	})
	res, err := svc.Create(context.Background(), paste.CreateInput{Content: []byte("x"), ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.attempts != 4 {
		t.Fatalf("attempts = %d, want 4", meta.attempts)
	}
	if _, content, err := svc.Get(context.Background(), res.Paste.ID); err != nil || string(content) != "x" {
		t.Fatalf("Get = %q, %v", content, err)
	}
}

func TestCreateGivesUpAfterRepeatedCollisions(t *testing.T) {
	svc := newServiceWith(t, func(m paste.MetaStore, b paste.BlobStore) (paste.MetaStore, paste.BlobStore) {
		return &collidingMeta{MetaStore: m, collisions: 100}, b
	})
	_, err := svc.Create(context.Background(), paste.CreateInput{Content: []byte("x"), ExpiresIn: time.Hour})
	if !errors.Is(err, paste.ErrIDSpaceExhaust) {
		t.Fatalf("err = %v, want ErrIDSpaceExhaust", err)
	}
}

func TestCreateRollsBackMetadataWhenBlobWriteFails(t *testing.T) {
	var meta *collidingMeta
	svc := newServiceWith(t, func(m paste.MetaStore, b paste.BlobStore) (paste.MetaStore, paste.BlobStore) {
		meta = &collidingMeta{MetaStore: m}
		return meta, failingBlobs{b}
	})
	_, err := svc.Create(context.Background(), paste.CreateInput{Content: []byte("x"), ExpiresIn: time.Hour})
	if err == nil {
		t.Fatal("Create should fail when the blob store fails")
	}
	if len(meta.deleted) != 1 {
		t.Fatalf("metadata rollbacks = %v, want exactly one", meta.deleted)
	}
	if _, _, err := svc.Get(context.Background(), meta.deleted[0]); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("rolled-back paste still readable: %v", err)
	}
}

func TestNewServiceValidatesOptions(t *testing.T) {
	db := openDB(t)
	for name, opts := range map[string]paste.Options{
		"short id":    {IDLength: 3, MaxPasteBytes: 1, Expiries: []time.Duration{time.Hour}},
		"zero size":   {IDLength: 10, MaxPasteBytes: 0, Expiries: []time.Duration{time.Hour}},
		"no expiries": {IDLength: 10, MaxPasteBytes: 1},
	} {
		if _, err := paste.NewService(db.Meta(), db.Blobs(), opts); err == nil {
			t.Errorf("%s: NewService accepted invalid options", name)
		}
	}
}
