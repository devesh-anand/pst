package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/devesh-anand/pst/internal/paste"
	"github.com/devesh-anand/pst/internal/store/sqlite"
	"github.com/devesh-anand/pst/web"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		header string
		reqHdr map[string]string
		remote string
		want   string
	}{
		{"remote addr when no header configured", "", nil, "203.0.113.7:5555", "203.0.113.7"},
		{"spoofed header ignored when not configured", "", map[string]string{"X-Real-IP": "1.2.3.4"}, "203.0.113.7:5555", "203.0.113.7"},
		{"configured header used", "X-Real-IP", map[string]string{"X-Real-IP": "198.51.100.9"}, "10.0.0.1:80", "198.51.100.9"},
		{"last forwarded hop wins", "X-Forwarded-For", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.9"}, "10.0.0.1:80", "198.51.100.9"},
		{"falls back when header missing", "X-Real-IP", nil, "10.0.0.1:80", "10.0.0.1"},
		{"ipv6 remote addr", "", nil, "[2001:db8::1]:443", "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: Config{ClientIPHeader: tc.header}}
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.reqHdr {
				r.Header.Set(k, v)
			}
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCreateRateLimit(t *testing.T) {
	ts := newTestServer(t, Config{})
	for i := range 30 {
		if res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil); res.status != http.StatusCreated {
			t.Fatalf("create %d = %d %s", i+1, res.status, res.body)
		}
	}
	if res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil); res.status != http.StatusTooManyRequests {
		t.Fatalf("31st create = %d, want 429", res.status)
	}
}

func TestDeleteRequiresToken(t *testing.T) {
	ts := newTestServer(t, Config{})
	id := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil).json(t)["id"].(string)
	if res := do(t, "DELETE", ts.URL+"/api/pastes/"+id, nil, nil); res.status != http.StatusBadRequest {
		t.Fatalf("delete without token = %d, want 400", res.status)
	}
	if res := do(t, "DELETE", ts.URL+"/api/pastes/AAAAAAAAAA", nil, map[string]string{"X-Delete-Token": "x"}); res.status != http.StatusNotFound {
		t.Fatalf("delete missing paste = %d, want 404", res.status)
	}
}

func TestOpenNonBurnPasteDoesNotDelete(t *testing.T) {
	ts := newTestServer(t, Config{})
	id := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil).json(t)["id"].(string)
	for range 2 {
		if res := do(t, "POST", ts.URL+"/api/pastes/"+id+"/open", nil, nil); res.status != http.StatusOK {
			t.Fatalf("open = %d", res.status)
		}
	}
}

func TestInvalidJSON(t *testing.T) {
	ts := newTestServer(t, Config{})
	res, err := http.Post(ts.URL+"/api/pastes", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestConfigEndpoint(t *testing.T) {
	ts := newTestServer(t, Config{})
	m := do(t, "GET", ts.URL+"/api/config", nil, nil).json(t)
	if m["maxPasteBytes"] != float64(1024) {
		t.Fatalf("maxPasteBytes = %v", m["maxPasteBytes"])
	}
	exp, _ := m["expiries"].([]any)
	if len(exp) != len(paste.DefaultExpiries) || exp[0] != float64(600) {
		t.Fatalf("expiries = %v", exp)
	}
}

// syncBuffer is a goroutine-safe log sink; handlers log from server goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogsNeverContainPasteIDs(t *testing.T) {
	var logs syncBuffer
	ts := newTestServerWithLog(t, Config{}, &logs)
	res := do(t, "POST", ts.URL+"/api/pastes", createBody(false), nil).json(t)
	id, token := res["id"].(string), res["deleteToken"].(string)
	do(t, "GET", ts.URL+"/"+id, nil, nil)
	do(t, "GET", ts.URL+"/api/pastes/"+id, nil, nil)
	do(t, "DELETE", ts.URL+"/api/pastes/"+id, nil, map[string]string{"X-Delete-Token": token})

	out := logs.String()
	if !strings.Contains(out, "GET /api/pastes/{id}") {
		t.Fatalf("expected route patterns in logs, got:\n%s", out)
	}
	if strings.Contains(out, id) || strings.Contains(out, token) {
		t.Fatalf("logs leaked a paste id or token:\n%s", out)
	}
}

func TestEmbeddedUI(t *testing.T) {
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "pst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := paste.NewService(db.Meta(), db.Blobs(), paste.Options{
		IDLength: 10, MaxPasteBytes: 1024, Expiries: slices.Clone(paste.DefaultExpiries),
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(svc, Config{}, web.Static(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New with embedded UI: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for path, want := range map[string]string{
		"/":                    "create.js",
		"/Abc123XYZ0":          "view.js",
		"/static/js/crypto.js": "AES-GCM",
		"/static/app.css":      "--accent",
		"/static/favicon.svg":  "<svg",
	} {
		res := do(t, "GET", ts.URL+path, nil, nil)
		if res.status != http.StatusOK || !strings.Contains(string(res.body), want) {
			t.Errorf("GET %s = %d, body missing %q", path, res.status, want)
		}
	}
}
