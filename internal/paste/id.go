package paste

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const (
	MinIDLength = 6
	MaxIDLength = 64
)

// NewID returns a uniformly random base62 string of length n, drawn from
// crypto/rand so IDs are not enumerable.
func NewID(n int) string {
	// Rejection sampling: 62 does not divide 256, so discard bytes >= 248
	// (the largest multiple of 62 below 256) to avoid modulo bias.
	const limit = 256 - 256%len(base62)
	out := make([]byte, 0, n)
	buf := make([]byte, n+n/4+8)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic("crypto/rand failed: " + err.Error())
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, base62[int(b)%len(base62)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}

// ValidID reports whether id has the shape of a paste ID. It is used to reject
// junk before it reaches storage.
func ValidID(id string) bool {
	if len(id) < MinIDLength || len(id) > MaxIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// newDeleteToken returns a 256-bit random token and its SHA-256 hash.
func newDeleteToken() (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func tokenMatches(token string, hash []byte) bool {
	return subtle.ConstantTimeCompare(hashToken(token), hash) == 1
}
