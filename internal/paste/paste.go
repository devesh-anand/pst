// Package paste holds the core domain: the Paste model, the storage
// interfaces that backends implement, and the Service that enforces the rules
// (expiry, burn-after-read, delete tokens, size limits).
//
// Paste content is opaque to the server. Clients encrypt before upload, so the
// service only ever stores and returns ciphertext envelopes.
package paste

import (
	"context"
	"errors"
	"time"
)

// Paste is the metadata for a stored paste. Content lives in a BlobStore under
// the same ID.
type Paste struct {
	ID              string
	CreatedAt       time.Time
	ExpiresAt       *time.Time // nil means the paste never expires
	BurnAfterRead   bool
	DeleteTokenHash []byte // SHA-256 of the delete token; the token itself is never stored
	Size            int
}

// Expired reports whether the paste is past its expiry at the given time.
func (p *Paste) Expired(now time.Time) bool {
	return p.ExpiresAt != nil && !now.Before(*p.ExpiresAt)
}

var (
	ErrNotFound       = errors.New("paste not found")
	ErrExists         = errors.New("paste id already exists")
	ErrEmpty          = errors.New("paste content is empty")
	ErrTooLarge       = errors.New("paste content is too large")
	ErrInvalidExpiry  = errors.New("expiry is not allowed")
	ErrInvalidToken   = errors.New("delete token is invalid")
	ErrIDSpaceExhaust = errors.New("could not allocate a unique paste id")
)

// MetaStore persists paste metadata. Implementations must make Create and
// Take atomic, since they back ID uniqueness and burn-after-read.
type MetaStore interface {
	// Create inserts p, returning ErrExists if the ID is already taken.
	Create(ctx context.Context, p *Paste) error
	// Get returns the paste or ErrNotFound. It does not filter expired pastes.
	Get(ctx context.Context, id string) (*Paste, error)
	// Take atomically fetches and deletes the paste, so exactly one concurrent
	// caller wins. Returns ErrNotFound if it does not exist.
	Take(ctx context.Context, id string) (*Paste, error)
	// Delete removes the paste, returning ErrNotFound if it does not exist.
	Delete(ctx context.Context, id string) error
	// DeleteExpired removes up to limit pastes that expired at or before now
	// and returns their IDs so their blobs can be cleaned up.
	DeleteExpired(ctx context.Context, now time.Time, limit int) ([]string, error)
}

// BlobStore persists paste content. It is keyed by paste ID so it can be
// swapped for an object store (S3, R2, filesystem) without touching metadata.
type BlobStore interface {
	Put(ctx context.Context, id string, data []byte) error
	// Get returns the content or ErrNotFound.
	Get(ctx context.Context, id string) ([]byte, error)
	// Delete removes the content. Deleting a missing blob is not an error.
	Delete(ctx context.Context, id string) error
}
