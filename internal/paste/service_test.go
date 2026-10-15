package paste_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devesh-anand/pst/internal/paste"
	"github.com/devesh-anand/pst/internal/store/sqlite"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func openDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "pst.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newService(t *testing.T, opts paste.Options) (*paste.Service, *clock) {
	t.Helper()
	svc, c, _ := newServiceDB(t, opts)
	return svc, c
}

func newServiceDB(t *testing.T, opts paste.Options) (*paste.Service, *clock, *sqlite.DB) {
	t.Helper()
	db := openDB(t)
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	if opts.IDLength == 0 {
		opts.IDLength = 10
	}
	if opts.MaxPasteBytes == 0 {
		opts.MaxPasteBytes = 1024
	}
	if opts.Expiries == nil {
		opts.Expiries = append([]time.Duration{paste.Never}, paste.DefaultExpiries...)
	}
	opts.Now = c.now
	svc, err := paste.NewService(db.Meta(), db.Blobs(), opts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, c, db
}

func TestCreateAndGet(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, paste.Options{})
	res, err := svc.Create(ctx, paste.CreateInput{Content: []byte("envelope"), ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(res.Paste.ID) != 10 || !paste.ValidID(res.Paste.ID) {
		t.Fatalf("bad id %q", res.Paste.ID)
	}
	if res.DeleteToken == "" {
		t.Fatal("empty delete token")
	}
	p, content, err := svc.Get(ctx, res.Paste.ID)
	if err != nil || string(content) != "envelope" || p.ExpiresAt == nil {
		t.Fatalf("Get = %+v, %q, %v", p, content, err)
	}
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, paste.Options{MaxPasteBytes: 4, Expiries: []time.Duration{time.Hour}})
	cases := []struct {
		name string
		in   paste.CreateInput
		want error
	}{
		{"empty", paste.CreateInput{ExpiresIn: time.Hour}, paste.ErrEmpty},
		{"too large", paste.CreateInput{Content: []byte("12345"), ExpiresIn: time.Hour}, paste.ErrTooLarge},
		{"never not allowed", paste.CreateInput{Content: []byte("x"), ExpiresIn: paste.Never}, paste.ErrInvalidExpiry},
		{"arbitrary expiry", paste.CreateInput{Content: []byte("x"), ExpiresIn: 3 * time.Hour}, paste.ErrInvalidExpiry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExpiredPasteIsGone(t *testing.T) {
	ctx := context.Background()
	svc, c := newService(t, paste.Options{})
	res, err := svc.Create(ctx, paste.CreateInput{Content: []byte("x"), ExpiresIn: 10 * time.Minute})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c.t = c.t.Add(10 * time.Minute)
	if _, _, err := svc.Get(ctx, res.Paste.ID); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("Get expired err = %v, want ErrNotFound", err)
	}
}

func TestSweep(t *testing.T) {
	ctx := context.Background()
	svc, c := newService(t, paste.Options{})
	short, _ := svc.Create(ctx, paste.CreateInput{Content: []byte("a"), ExpiresIn: 10 * time.Minute})
	long, _ := svc.Create(ctx, paste.CreateInput{Content: []byte("b"), ExpiresIn: time.Hour})
	forever, _ := svc.Create(ctx, paste.CreateInput{Content: []byte("c"), ExpiresIn: paste.Never})

	c.t = c.t.Add(30 * time.Minute)
	n, err := svc.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1", n, err)
	}
	if _, _, err := svc.Get(ctx, short.Paste.ID); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("short paste survived sweep: %v", err)
	}
	for _, id := range []string{long.Paste.ID, forever.Paste.ID} {
		if _, _, err := svc.Get(ctx, id); err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
	}
}

func TestBurnAfterRead(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, paste.Options{})
	res, err := svc.Create(ctx, paste.CreateInput{Content: []byte("secret"), ExpiresIn: time.Hour, BurnAfterRead: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := res.Paste.ID

	// A plain Get (what a link-preview bot does) must not reveal or burn it.
	for range 3 {
		p, content, err := svc.Get(ctx, id)
		if err != nil || !p.BurnAfterRead || content != nil {
			t.Fatalf("Get = %+v, %q, %v; want metadata only", p, content, err)
		}
	}

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, content, err := svc.Open(ctx, id)
			switch {
			case err == nil && string(content) == "secret":
				wins.Add(1)
			case errors.Is(err, paste.ErrNotFound):
			default:
				t.Errorf("Open: %q, %v", content, err)
			}
		}()
	}
	wg.Wait()
	if n := wins.Load(); n != 1 {
		t.Fatalf("Open winners = %d, want 1", n)
	}
	if _, _, err := svc.Get(ctx, id); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("Get after burn err = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, paste.Options{})
	res, err := svc.Create(ctx, paste.CreateInput{Content: []byte("x"), ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, res.Paste.ID, "wrong"); !errors.Is(err, paste.ErrInvalidToken) {
		t.Fatalf("Delete wrong token err = %v, want ErrInvalidToken", err)
	}
	if err := svc.Delete(ctx, res.Paste.ID, res.DeleteToken); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := svc.Get(ctx, res.Paste.ID); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
}

func TestNewIDIsRandomAndValid(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := paste.NewID(12)
		if !paste.ValidID(id) {
			t.Fatalf("invalid id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "abc", "abc-def", "abc/def12", "../../etc"} {
		if paste.ValidID(bad) {
			t.Fatalf("ValidID(%q) = true", bad)
		}
	}
}

func TestOpenExpiredBurnPaste(t *testing.T) {
	ctx := context.Background()
	svc, c, db := newServiceDB(t, paste.Options{})
	res, err := svc.Create(ctx, paste.CreateInput{Content: []byte("x"), ExpiresIn: 10 * time.Minute, BurnAfterRead: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c.t = c.t.Add(time.Hour)
	if _, _, err := svc.Open(ctx, res.Paste.ID); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("Open expired err = %v, want ErrNotFound", err)
	}
	if _, err := db.Blobs().Get(ctx, res.Paste.ID); !errors.Is(err, paste.ErrNotFound) {
		t.Fatalf("expired blob not cleaned up: %v", err)
	}
}

func TestSweepAndDeleteRemoveContent(t *testing.T) {
	ctx := context.Background()
	svc, c, db := newServiceDB(t, paste.Options{})
	expiring, _ := svc.Create(ctx, paste.CreateInput{Content: []byte("a"), ExpiresIn: 10 * time.Minute})
	deleted, _ := svc.Create(ctx, paste.CreateInput{Content: []byte("b"), ExpiresIn: time.Hour})

	if err := svc.Delete(ctx, deleted.Paste.ID, deleted.DeleteToken); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	c.t = c.t.Add(time.Hour - time.Second)
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, id := range []string{expiring.Paste.ID, deleted.Paste.ID} {
		if _, err := db.Blobs().Get(ctx, id); !errors.Is(err, paste.ErrNotFound) {
			t.Fatalf("blob %s still stored: %v", id, err)
		}
	}
}

func TestSweepHandlesMoreThanOneBatch(t *testing.T) {
	ctx := context.Background()
	svc, c := newService(t, paste.Options{})
	const n = 1203
	for i := range n {
		if _, err := svc.Create(ctx, paste.CreateInput{Content: []byte("x"), ExpiresIn: 10 * time.Minute}); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	c.t = c.t.Add(time.Hour)
	if got, err := svc.Sweep(ctx); err != nil || got != n {
		t.Fatalf("Sweep = %d, %v; want %d", got, err, n)
	}
}
