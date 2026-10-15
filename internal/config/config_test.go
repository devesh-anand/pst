package config

import (
	"strings"
	"testing"
)

// clearEnv unsets every PST_ variable for the duration of the test.
func clearEnv(t *testing.T) {
	for _, k := range []string{
		"PST_ADDR", "PST_DB_PATH", "PST_META_STORE", "PST_BLOB_STORE", "PST_ID_LENGTH",
		"PST_MAX_PASTE_BYTES", "PST_ALLOW_NEVER_EXPIRE", "PST_REQUIRE_WRITE_PASSWORD",
		"PST_WRITE_PASSWORD", "PST_CLIENT_IP_HEADER",
	} {
		t.Setenv(k, "")
	}
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Addr: ":8080", DBPath: "data/pst.db", MetaStore: "sqlite", BlobStore: "sqlite",
		IDLength: 10, MaxPasteBytes: 2 << 20, AllowNever: true,
	}
	if c != want {
		t.Fatalf("Load = %+v, want %+v", c, want)
	}
}

func TestOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("PST_ADDR", "127.0.0.1:9000")
	t.Setenv("PST_ID_LENGTH", "16")
	t.Setenv("PST_MAX_PASTE_BYTES", "1024")
	t.Setenv("PST_ALLOW_NEVER_EXPIRE", "false")
	t.Setenv("PST_REQUIRE_WRITE_PASSWORD", "true")
	t.Setenv("PST_WRITE_PASSWORD", "twelve-chars-or-more")
	t.Setenv("PST_CLIENT_IP_HEADER", "X-Real-IP")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "127.0.0.1:9000" || c.IDLength != 16 || c.MaxPasteBytes != 1024 || c.AllowNever ||
		!c.RequireWritePassword || c.WritePassword != "twelve-chars-or-more" || c.ClientIPHeader != "X-Real-IP" {
		t.Fatalf("Load = %+v", c)
	}
}

func TestInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"bad int", map[string]string{"PST_ID_LENGTH": "ten"}, "PST_ID_LENGTH"},
		{"bad bool", map[string]string{"PST_ALLOW_NEVER_EXPIRE": "sometimes"}, "PST_ALLOW_NEVER_EXPIRE"},
		{"flag without password", map[string]string{"PST_REQUIRE_WRITE_PASSWORD": "true"}, "PST_WRITE_PASSWORD"},
		{"flag with short password", map[string]string{"PST_REQUIRE_WRITE_PASSWORD": "1", "PST_WRITE_PASSWORD": "short"}, "at least 12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load err = %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}

func TestPasswordIgnoredWhenFlagOff(t *testing.T) {
	clearEnv(t)
	t.Setenv("PST_WRITE_PASSWORD", "short")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with flag off should not validate the password: %v", err)
	}
}
