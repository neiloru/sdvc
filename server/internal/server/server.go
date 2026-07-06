// Package server exposes the sdvc store over HTTP.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"sdvc/server/internal/store"
)

// Options configures the HTTP server.
type Options struct {
	// MaxUploadBytes caps the size of an uploaded archive. Zero means unlimited.
	MaxUploadBytes int64
}

// Server wires the store to HTTP handlers.
type Server struct {
	store *store.Store
	opts  Options
}

// New creates a Server.
func New(st *store.Store, opts Options) *Server {
	return &Server{store: st, opts: opts}
}

// Handler returns the fully configured HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/repos/{user}", s.handleListRepos)
	mux.HandleFunc("POST /v1/repos/{user}/{repo}", s.handleUpload)
	mux.HandleFunc("GET /v1/repos/{user}/{repo}/versions", s.handleListVersions)
	mux.HandleFunc("GET /v1/repos/{user}/{repo}/versions/{version}", s.handleDownloadVersion)
	mux.HandleFunc("GET /v1/repos/{user}/{repo}/latest", s.handleDownloadLatest)

	return logRequests(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	refs, err := s.store.ListRepos(user)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "repos": refs})
}

// handleUpload stores a new archive version.
//
// The expected SHA-256 hash (hex) must be supplied via the "X-Content-Sha256"
// header or the "hash" query parameter. The request body is the raw zip file.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	repo := r.PathValue("repo")

	expected := r.Header.Get("X-Content-Sha256")
	if expected == "" {
		expected = r.URL.Query().Get("hash")
	}

	body := r.Body
	if s.opts.MaxUploadBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	}

	info, err := s.store.Save(user, repo, body, expected)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	repo := r.PathValue("repo")

	idx, err := s.store.ListVersions(user, repo)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

func (s *Server) handleDownloadVersion(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	repo := r.PathValue("repo")

	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}

	f, info, err := s.store.Open(user, repo, version)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	defer f.Close()
	serveArchive(w, r, f, info)
}

func (s *Server) handleDownloadLatest(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	repo := r.PathValue("repo")

	f, info, err := s.store.OpenLatest(user, repo)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	defer f.Close()
	serveArchive(w, r, f, info)
}

// serveArchive streams a zip archive with appropriate headers and range support.
func serveArchive(w http.ResponseWriter, r *http.Request, f *os.File, info store.VersionInfo) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", info.Filename))
	w.Header().Set("X-Content-Sha256", info.Hash)
	w.Header().Set("X-Version", strconv.Itoa(info.Version))
	http.ServeContent(w, r, info.Filename, info.Timestamp, f)
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "invalid user or repo name")
	case errors.Is(err, store.ErrHashRequired):
		writeError(w, http.StatusBadRequest, "expected SHA-256 hash is required (X-Content-Sha256 header or ?hash=)")
	case errors.Is(err, store.ErrHashMismatch):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrEmptyUpload):
		writeError(w, http.StatusBadRequest, "uploaded archive is empty")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "upload exceeds maximum allowed size")
			return
		}
		log.Printf("internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
