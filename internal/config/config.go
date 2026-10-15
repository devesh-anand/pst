// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr          string // PST_ADDR, else ":$PORT" (set by most PaaS hosts), else ":8080"
	DBPath        string // PST_DB_PATH
	MetaStore     string // PST_META_STORE: backend for paste metadata ("sqlite")
	BlobStore     string // PST_BLOB_STORE: backend for paste content ("sqlite")
	IDLength      int    // PST_ID_LENGTH
	MaxPasteBytes int    // PST_MAX_PASTE_BYTES
	AllowNever    bool   // PST_ALLOW_NEVER_EXPIRE

	// When RequireWritePassword is set, creating a paste requires
	// WritePassword. Reads stay open to anyone with the link.
	RequireWritePassword bool   // PST_REQUIRE_WRITE_PASSWORD
	WritePassword        string // PST_WRITE_PASSWORD

	// ClientIPHeader names a header set by a trusted reverse proxy that holds
	// the real client IP (e.g. "X-Real-IP" or "CF-Connecting-IP"). Leave empty
	// when not behind a proxy; it is otherwise spoofable.
	ClientIPHeader string // PST_CLIENT_IP_HEADER
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	c := Config{
		Addr:           env("PST_ADDR", ":"+env("PORT", "8080")),
		DBPath:         env("PST_DB_PATH", "data/pst.db"),
		MetaStore:      env("PST_META_STORE", "sqlite"),
		BlobStore:      env("PST_BLOB_STORE", "sqlite"),
		WritePassword:  os.Getenv("PST_WRITE_PASSWORD"),
		ClientIPHeader: os.Getenv("PST_CLIENT_IP_HEADER"),
	}
	var errs []error
	c.IDLength = envInt("PST_ID_LENGTH", 10, &errs)
	c.MaxPasteBytes = envInt("PST_MAX_PASTE_BYTES", 2<<20, &errs)
	c.AllowNever = envBool("PST_ALLOW_NEVER_EXPIRE", true, &errs)
	c.RequireWritePassword = envBool("PST_REQUIRE_WRITE_PASSWORD", false, &errs)

	if c.RequireWritePassword && len(c.WritePassword) < 12 {
		errs = append(errs, errors.New("PST_WRITE_PASSWORD must be at least 12 characters when PST_REQUIRE_WRITE_PASSWORD is true"))
	}
	return c, errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int, errs *[]error) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

func envBool(key string, def bool, errs *[]error) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a boolean", key, v))
		return def
	}
	return b
}
