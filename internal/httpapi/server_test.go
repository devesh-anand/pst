package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devesh-anand/pst/internal/paste"
	"github.com/devesh-anand/pst/internal/store/sqlite"
)

const writePassword = "correct horse battery staple"

func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	return newTestServerWithLog(t, cfg, io.Discard)
}

func newTestServerWithLog(t *testing.T, cfg Config, logOut io.Writer) *httptest.Server {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "pst.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := paste.NewService(db.Meta(), db.Blobs(), paste.Options{
		IDLength:      10,
		MaxPasteBytes: 1024,
		Expiries:      slices.Clone(paste.DefaultExpiries),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	static := fstest.MapFS{
		"index.html":   {Data: []byte("index page")},
		"view.html":    {Data: []byte("view page")},
		"js/crypto.js": {Data: []byte("// js")},
	}
	s, err := New(svc, cfg, static, slog.New(slog.NewTextHandler(logOut, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return m
}

func do(t *testing.T, method, url string, body any, headers map[string]string) response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return response{status: res.StatusCode, header: res.Header, body: b}
}

func createBody(burn bool) map[string]any {
	return map[string]any{"content": "hdr.ciphertext", "expiresIn": 3600, "burnAfterRead": burn}
}

func TestCreateGetDelete(t *testing.T) {
	ts := newTestServer(t, Config{})

	res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", res.status, res.body)
	}
	created := res.json(t)
	id, token := created["id"].(string), created["deleteToken"].(string)

	res = do(t, "GET", ts.URL+"/api/pastes/"+id, nil, nil)
	if res.status != http.StatusOK || res.json(t)["content"] != "hdr.ciphertext" {
		t.Fatalf("get = %d %s", res.status, res.body)
	}
	if got := res.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if _, leaked := res.json(t)["deleteToken"]; leaked {
		t.Fatal("GET leaked the delete token")
	}

	if res = do(t, "DELETE", ts.URL+"/api/pastes/"+id, nil, map[string]string{"X-Delete-Token": "nope"}); res.status != http.StatusForbidden {
		t.Fatalf("delete with bad token = %d", res.status)
	}
	if res = do(t, "DELETE", ts.URL+"/api/pastes/"+id, nil, map[string]string{"X-Delete-Token": token}); res.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", res.status, res.body)
	}
	if res = do(t, "GET", ts.URL+"/api/pastes/"+id, nil, nil); res.status != http.StatusNotFound {
		t.Fatalf("get after delete = %d", res.status)
	}
}

func TestBurnAfterReadNeedsExplicitOpen(t *testing.T) {
	ts := newTestServer(t, Config{})
	id := do(t, "POST", ts.URL+"/api/pastes", createBody(true), nil).json(t)["id"].(string)

	// GET (what link unfurlers do) returns metadata only and does not burn.
	for range 2 {
		res := do(t, "GET", ts.URL+"/api/pastes/"+id, nil, nil)
		m := res.json(t)
		if res.status != http.StatusOK || m["burnAfterRead"] != true || m["content"] != nil {
			t.Fatalf("get burn paste = %d %s", res.status, res.body)
		}
	}
	res := do(t, "POST", ts.URL+"/api/pastes/"+id+"/open", nil, nil)
	if res.status != http.StatusOK || res.json(t)["content"] != "hdr.ciphertext" {
		t.Fatalf("open = %d %s", res.status, res.body)
	}
	if res = do(t, "POST", ts.URL+"/api/pastes/"+id+"/open", nil, nil); res.status != http.StatusNotFound {
		t.Fatalf("second open = %d, want 404", res.status)
	}
}

func TestWritePasswordFlag(t *testing.T) {
	t.Run("disabled allows anonymous writes", func(t *testing.T) {
		ts := newTestServer(t, Config{})
		if res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil); res.status != http.StatusCreated {
			t.Fatalf("create = %d %s", res.status, res.body)
		}
		if got := do(t, "GET", ts.URL+"/api/config", nil, nil).json(t)["writePasswordRequired"]; got != false {
			t.Fatalf("config writePasswordRequired = %v", got)
		}
	})

	t.Run("enabled requires password for writes only", func(t *testing.T) {
		ts := newTestServer(t, Config{RequireWritePassword: true, WritePassword: writePassword})
		if got := do(t, "GET", ts.URL+"/api/config", nil, nil).json(t)["writePasswordRequired"]; got != true {
			t.Fatalf("config writePasswordRequired = %v", got)
		}

		res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil)
		if res.status != http.StatusUnauthorized || res.header.Get("WWW-Authenticate") == "" {
			t.Fatalf("no password = %d", res.status)
		}
		res = do(t, "POST", ts.URL+"/api/pastes", createBody(false), map[string]string{"Authorization": "Bearer wrong"})
		if res.status != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d", res.status)
		}
		res = do(t, "POST", ts.URL+"/api/pastes", createBody(false), map[string]string{"Authorization": "Bearer " + writePassword})
		if res.status != http.StatusCreated {
			t.Fatalf("right password = %d %s", res.status, res.body)
		}

		// Reads work without the password.
		id := res.json(t)["id"].(string)
		if res := do(t, "GET", ts.URL+"/api/pastes/"+id, nil, nil); res.status != http.StatusOK {
			t.Fatalf("anonymous read = %d", res.status)
		}
	})

	t.Run("repeated failures are rate limited", func(t *testing.T) {
		ts := newTestServer(t, Config{RequireWritePassword: true, WritePassword: writePassword})
		bad := map[string]string{"Authorization": "Bearer wrong"}
		for i := range 10 {
			if res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), bad); res.status != http.StatusUnauthorized {
				t.Fatalf("attempt %d = %d", i, res.status)
			}
		}
		// Locked out even with the right password until the window passes.
		res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), map[string]string{"Authorization": "Bearer " + writePassword})
		if res.status != http.StatusTooManyRequests {
			t.Fatalf("after lockout = %d, want 429", res.status)
		}
	})
}

func TestCreateValidation(t *testing.T) {
	ts := newTestServer(t, Config{})
	cases := []struct {
		name string
		body any
		want int
	}{
		{"missing expiry", map[string]any{"content": "x"}, http.StatusBadRequest},
		{"never not allowed", map[string]any{"content": "x", "expiresIn": 0}, http.StatusBadRequest},
		{"odd expiry", map[string]any{"content": "x", "expiresIn": 123}, http.StatusBadRequest},
		{"empty", map[string]any{"content": "", "expiresIn": 3600}, http.StatusBadRequest},
		{"unknown field", map[string]any{"content": "x", "expiresIn": 3600, "admin": true}, http.StatusBadRequest},
		{"too large", map[string]any{"content": strings.Repeat("a", 1025), "expiresIn": 3600}, http.StatusRequestEntityTooLarge},
		{"way too large", map[string]any{"content": strings.Repeat("a", 10000), "expiresIn": 3600}, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if res := do(t, "POST", ts.URL+"/api/pastes", tc.body, nil); res.status != tc.want {
				t.Fatalf("status = %d, want %d (%s)", res.status, tc.want, res.body)
			}
		})
	}
}

func TestPagesAndHeaders(t *testing.T) {
	ts := newTestServer(t, Config{})
	cases := []struct {
		path, wantBody string
		wantStatus     int
	}{
		{"/", "index page", http.StatusOK},
		{"/Abc123XYZ0", "view page", http.StatusOK},
		{"/static/js/crypto.js", "// js", http.StatusOK},
		{"/not-an-id!", "", http.StatusNotFound},
		{"/api/pastes/../../etc", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		res := do(t, "GET", ts.URL+tc.path, nil, nil)
		if res.status != tc.wantStatus || (tc.wantBody != "" && string(res.body) != tc.wantBody) {
			t.Errorf("GET %s = %d %q", tc.path, res.status, res.body)
		}
		if csp := res.header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("GET %s CSP = %q", tc.path, csp)
		}
		if res.header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %s missing Referrer-Policy", tc.path)
		}
	}
}
