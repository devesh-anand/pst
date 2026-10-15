// Command pst runs the paste server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/devesh-anand/pst/internal/config"
	"github.com/devesh-anand/pst/internal/httpapi"
	"github.com/devesh-anand/pst/internal/paste"
	"github.com/devesh-anand/pst/internal/store/sqlite"
	"github.com/devesh-anand/pst/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	meta, blobs, closeStores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStores()

	expiries := slices.Clone(paste.DefaultExpiries)
	if cfg.AllowNever {
		expiries = append(expiries, paste.Never)
	}
	svc, err := paste.NewService(meta, blobs, paste.Options{
		IDLength:      cfg.IDLength,
		MaxPasteBytes: cfg.MaxPasteBytes,
		Expiries:      expiries,
		Logger:        log,
	})
	if err != nil {
		return err
	}
	go svc.RunSweeper(ctx, time.Minute)

	api, err := httpapi.New(svc, httpapi.Config{
		RequireWritePassword: cfg.RequireWritePassword,
		WritePassword:        cfg.WritePassword,
		ClientIPHeader:       cfg.ClientIPHeader,
	}, web.Static(), log)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("pst listening", "addr", cfg.Addr, "write_password_required", cfg.RequireWritePassword,
		"meta_store", cfg.MetaStore, "blob_store", cfg.BlobStore)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openStores builds the metadata and content backends named in the config.
// Add new backends (postgres, s3, ...) here; each only needs to implement
// paste.MetaStore or paste.BlobStore and pass the pastetest suite.
func openStores(ctx context.Context, cfg config.Config) (paste.MetaStore, paste.BlobStore, func(), error) {
	var closers []io.Closer
	closeAll := func() {
		for _, c := range closers {
			c.Close()
		}
	}

	var db *sqlite.DB
	sqliteDB := func() (*sqlite.DB, error) {
		if db != nil {
			return db, nil
		}
		var err error
		db, err = sqlite.Open(ctx, cfg.DBPath)
		if err == nil {
			closers = append(closers, db)
		}
		return db, err
	}

	var meta paste.MetaStore
	switch cfg.MetaStore {
	case "sqlite":
		d, err := sqliteDB()
		if err != nil {
			return nil, nil, nil, err
		}
		meta = d.Meta()
	default:
		return nil, nil, nil, fmt.Errorf("unknown PST_META_STORE %q", cfg.MetaStore)
	}

	var blobs paste.BlobStore
	switch cfg.BlobStore {
	case "sqlite":
		d, err := sqliteDB()
		if err != nil {
			closeAll()
			return nil, nil, nil, err
		}
		blobs = d.Blobs()
	default:
		closeAll()
		return nil, nil, nil, fmt.Errorf("unknown PST_BLOB_STORE %q", cfg.BlobStore)
	}
	return meta, blobs, closeAll, nil
}
