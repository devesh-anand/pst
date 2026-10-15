package paste

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// Never is the expiry value for pastes that do not expire.
const Never time.Duration = 0

// DefaultExpiries are the expiry choices offered to clients, shortest first.
var DefaultExpiries = []time.Duration{
	10 * time.Minute,
	time.Hour,
	24 * time.Hour,
	7 * 24 * time.Hour,
	30 * 24 * time.Hour,
	365 * 24 * time.Hour,
}

// Options configures a Service.
type Options struct {
	IDLength      int
	MaxPasteBytes int
	Expiries      []time.Duration // allowed expiries; include Never to allow permanent pastes
	Now           func() time.Time
	Logger        *slog.Logger
}

// Service implements paste use cases on top of a MetaStore and a BlobStore.
type Service struct {
	meta  MetaStore
	blobs BlobStore
	opts  Options
}

func NewService(meta MetaStore, blobs BlobStore, opts Options) (*Service, error) {
	if opts.IDLength < MinIDLength || opts.IDLength > MaxIDLength {
		return nil, fmt.Errorf("id length must be between %d and %d", MinIDLength, MaxIDLength)
	}
	if opts.MaxPasteBytes <= 0 {
		return nil, errors.New("max paste bytes must be positive")
	}
	if len(opts.Expiries) == 0 {
		return nil, errors.New("at least one expiry must be allowed")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{meta: meta, blobs: blobs, opts: opts}, nil
}

// Expiries returns the allowed expiry choices.
func (s *Service) Expiries() []time.Duration { return slices.Clone(s.opts.Expiries) }

// MaxPasteBytes returns the maximum accepted content size.
func (s *Service) MaxPasteBytes() int { return s.opts.MaxPasteBytes }

type CreateInput struct {
	Content       []byte
	ExpiresIn     time.Duration // Never for no expiry
	BurnAfterRead bool
}

type CreateResult struct {
	Paste       *Paste
	DeleteToken string // returned once to the creator; only its hash is stored
}

// Create stores a new paste under a fresh random ID.
func (s *Service) Create(ctx context.Context, in CreateInput) (*CreateResult, error) {
	if len(in.Content) == 0 {
		return nil, ErrEmpty
	}
	if len(in.Content) > s.opts.MaxPasteBytes {
		return nil, ErrTooLarge
	}
	if !slices.Contains(s.opts.Expiries, in.ExpiresIn) {
		return nil, ErrInvalidExpiry
	}

	now := s.opts.Now().UTC().Truncate(time.Second)
	token, tokenHash := newDeleteToken()
	p := &Paste{
		CreatedAt:       now,
		BurnAfterRead:   in.BurnAfterRead,
		DeleteTokenHash: tokenHash,
		Size:            len(in.Content),
	}
	if in.ExpiresIn != Never {
		exp := now.Add(in.ExpiresIn)
		p.ExpiresAt = &exp
	}

	// Reserve the ID in the metadata store first. Writing the blob first could
	// overwrite another paste's content on an ID collision.
	const maxAttempts = 5
	for attempt := 0; ; attempt++ {
		if attempt == maxAttempts {
			return nil, ErrIDSpaceExhaust
		}
		p.ID = NewID(s.opts.IDLength)
		err := s.meta.Create(ctx, p)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrExists) {
			return nil, fmt.Errorf("create paste metadata: %w", err)
		}
	}

	if err := s.blobs.Put(ctx, p.ID, in.Content); err != nil {
		if delErr := s.meta.Delete(ctx, p.ID); delErr != nil {
			s.opts.Logger.Error("rollback paste metadata", "err", delErr)
		}
		return nil, fmt.Errorf("store paste content: %w", err)
	}
	return &CreateResult{Paste: p, DeleteToken: token}, nil
}

// Get returns a paste and its content. For burn-after-read pastes it returns
// only the metadata (content is nil): reading those requires Open, so that
// link-preview bots doing a plain GET cannot burn them.
func (s *Service) Get(ctx context.Context, id string) (*Paste, []byte, error) {
	p, err := s.meta.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if p.Expired(s.opts.Now()) {
		s.remove(ctx, id)
		return nil, nil, ErrNotFound
	}
	if p.BurnAfterRead {
		return p, nil, nil
	}
	content, err := s.blobs.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return p, content, nil
}

// Open returns a paste's content, deleting it first if it is burn-after-read.
// Concurrent Opens of a burn-after-read paste have exactly one winner.
func (s *Service) Open(ctx context.Context, id string) (*Paste, []byte, error) {
	p, err := s.meta.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !p.BurnAfterRead {
		return s.Get(ctx, id)
	}

	p, err = s.meta.Take(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	content, err := s.blobs.Get(ctx, id)
	s.deleteBlob(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if p.Expired(s.opts.Now()) {
		return nil, nil, ErrNotFound
	}
	return p, content, nil
}

// Delete removes a paste if token matches the one issued at creation.
func (s *Service) Delete(ctx context.Context, id, token string) error {
	p, err := s.meta.Get(ctx, id)
	if err != nil {
		return err
	}
	if !tokenMatches(token, p.DeleteTokenHash) {
		return ErrInvalidToken
	}
	if err := s.meta.Delete(ctx, id); err != nil {
		return err
	}
	s.deleteBlob(ctx, id)
	return nil
}

// Sweep deletes expired pastes in batches and returns how many were removed.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	const batch = 500
	total := 0
	for {
		ids, err := s.meta.DeleteExpired(ctx, s.opts.Now(), batch)
		if err != nil {
			return total, err
		}
		for _, id := range ids {
			s.deleteBlob(ctx, id)
		}
		total += len(ids)
		if len(ids) < batch {
			return total, nil
		}
	}
}

// RunSweeper calls Sweep every interval until ctx is cancelled.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.Sweep(ctx)
			if err != nil && ctx.Err() == nil {
				s.opts.Logger.Error("sweep expired pastes", "err", err)
			} else if n > 0 {
				s.opts.Logger.Info("swept expired pastes", "count", n)
			}
		}
	}
}

func (s *Service) remove(ctx context.Context, id string) {
	if err := s.meta.Delete(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		s.opts.Logger.Error("delete paste metadata", "err", err)
		return
	}
	s.deleteBlob(ctx, id)
}

func (s *Service) deleteBlob(ctx context.Context, id string) {
	if err := s.blobs.Delete(ctx, id); err != nil {
		// An orphaned blob is unreachable without its metadata, so this is a
		// storage leak rather than a data exposure.
		s.opts.Logger.Error("delete paste content", "err", err)
	}
}
