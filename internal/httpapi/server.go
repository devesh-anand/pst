// Package httpapi exposes the paste service over HTTP and serves the web UI.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/devesh-anand/pst/internal/paste"
)

type Config struct {
	RequireWritePassword bool
	WritePassword        string
	ClientIPHeader       string
}

type Server struct {
	svc    *paste.Service
	cfg    Config
	static fs.FS
	log    *slog.Logger

	indexHTML, viewHTML []byte
	writePasswordHash   [32]byte

	createLimit  *limiter // paste creations per IP
	authFailures *limiter // wrong write passwords per IP
	readLimit    *limiter // reads/opens/deletes per IP, slows ID enumeration
}

// New builds a Server. static must contain index.html and view.html.
func New(svc *paste.Service, cfg Config, static fs.FS, log *slog.Logger) (*Server, error) {
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return nil, err
	}
	view, err := fs.ReadFile(static, "view.html")
	if err != nil {
		return nil, err
	}
	if cfg.RequireWritePassword && cfg.WritePassword == "" {
		return nil, errors.New("write password required but not set")
	}
	return &Server{
		svc:               svc,
		cfg:               cfg,
		static:            static,
		log:               log,
		indexHTML:         index,
		viewHTML:          view,
		writePasswordHash: sha256.Sum256([]byte(cfg.WritePassword)),
		createLimit:       newLimiter(30, time.Minute),
		authFailures:      newLimiter(10, 15*time.Minute),
		readLimit:         newLimiter(300, time.Minute),
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/pastes", s.handleCreate)
	mux.HandleFunc("GET /api/pastes/{id}", s.handleGet)
	mux.HandleFunc("POST /api/pastes/{id}/open", s.handleOpen)
	mux.HandleFunc("DELETE /api/pastes/{id}", s.handleDelete)

	staticFiles := http.StripPrefix("/static/", http.FileServerFS(s.static))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		staticFiles.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { serveHTML(w, s.indexHTML) })
	mux.HandleFunc("GET /{id}", func(w http.ResponseWriter, r *http.Request) {
		if !paste.ValidID(r.PathValue("id")) {
			http.NotFound(w, r)
			return
		}
		serveHTML(w, s.viewHTML)
	})

	return s.logRequests(securityHeaders(mux))
}

type configResponse struct {
	WritePasswordRequired bool    `json:"writePasswordRequired"`
	MaxPasteBytes         int     `json:"maxPasteBytes"`
	Expiries              []int64 `json:"expiries"` // seconds; 0 means never
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := configResponse{
		WritePasswordRequired: s.cfg.RequireWritePassword,
		MaxPasteBytes:         s.svc.MaxPasteBytes(),
	}
	for _, d := range s.svc.Expiries() {
		resp.Expiries = append(resp.Expiries, int64(d/time.Second))
	}
	writeJSON(w, http.StatusOK, resp)
}

type createRequest struct {
	Content       string `json:"content"`
	ExpiresIn     *int64 `json:"expiresIn"` // seconds; 0 means never
	BurnAfterRead bool   `json:"burnAfterRead"`
}

type pasteResponse struct {
	ID            string     `json:"id"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	BurnAfterRead bool       `json:"burnAfterRead"`
	Content       *string    `json:"content,omitempty"`
	DeleteToken   string     `json:"deleteToken,omitempty"`
}

func toResponse(p *paste.Paste, content []byte) pasteResponse {
	resp := pasteResponse{ID: p.ID, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, BurnAfterRead: p.BurnAfterRead}
	if content != nil {
		c := string(content)
		resp.Content = &c
	}
	return resp
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.cfg.RequireWritePassword {
		if s.authFailures.Blocked(ip) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed password attempts, try again later")
			return
		}
		if !s.writePasswordOK(r) {
			s.authFailures.Allow(ip)
			w.Header().Set("WWW-Authenticate", `Bearer realm="pst"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid write password is required")
			return
		}
	}
	if !s.createLimit.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many pastes, slow down")
		return
	}

	// Allow headroom over the content limit for the JSON wrapper.
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.svc.MaxPasteBytes())+4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req createRequest
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "paste is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if req.ExpiresIn == nil || *req.ExpiresIn < 0 {
		writeError(w, http.StatusBadRequest, "invalid_expiry", "expiresIn is required")
		return
	}

	res, err := s.svc.Create(r.Context(), paste.CreateInput{
		Content:       []byte(req.Content),
		ExpiresIn:     time.Duration(*req.ExpiresIn) * time.Second,
		BurnAfterRead: req.BurnAfterRead,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	resp := toResponse(res.Paste, nil)
	resp.DeleteToken = res.DeleteToken
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := s.readPreamble(w, r)
	if !ok {
		return
	}
	p, content, err := s.svc.Get(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(p, content))
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	id, ok := s.readPreamble(w, r)
	if !ok {
		return
	}
	p, content, err := s.svc.Open(r.Context(), id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(p, content))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.readPreamble(w, r)
	if !ok {
		return
	}
	token := r.Header.Get("X-Delete-Token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing_token", "X-Delete-Token header is required")
		return
	}
	if err := s.svc.Delete(r.Context(), id, token); err != nil {
		s.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readPreamble applies the per-IP read limit and validates the path ID.
func (s *Server) readPreamble(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.readLimit.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
		return "", false
	}
	id := r.PathValue("id")
	if !paste.ValidID(id) {
		writeError(w, http.StatusNotFound, "not_found", "paste not found")
		return "", false
	}
	return id, true
}

func (s *Server) writePasswordOK(r *http.Request) bool {
	given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	// Hash both sides so the comparison is constant-time regardless of length.
	sum := sha256.Sum256([]byte(given))
	return subtle.ConstantTimeCompare(sum[:], s.writePasswordHash[:]) == 1
}

func (s *Server) clientIP(r *http.Request) string {
	if h := s.cfg.ClientIPHeader; h != "" {
		if v := r.Header.Get(h); v != "" {
			// For X-Forwarded-For style lists, the last entry is the one
			// appended by our own proxy; earlier ones are client-controlled.
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, paste.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "paste not found")
	case errors.Is(err, paste.ErrEmpty):
		writeError(w, http.StatusBadRequest, "empty", "paste content is empty")
	case errors.Is(err, paste.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "paste is too large")
	case errors.Is(err, paste.ErrInvalidExpiry):
		writeError(w, http.StatusBadRequest, "invalid_expiry", "expiry is not allowed")
	case errors.Is(err, paste.ErrInvalidToken):
		writeError(w, http.StatusForbidden, "invalid_token", "delete token is invalid")
	default:
		s.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func serveHTML(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}
