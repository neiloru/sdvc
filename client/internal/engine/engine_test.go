package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"sdvc/client/internal/config"
)

// stubServer is a minimal in-memory implementation of the sdvc server API,
// enough to exercise the client's upload/download/hash-verify flow.
type stubServer struct {
	mu    sync.Mutex
	store map[string][][]byte // "user/repo" -> versions (raw zip bytes), 1-indexed
}

func newStubServer() *httptest.Server {
	s := &stubServer{store: make(map[string][][]byte)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/repos/{user}/{repo}", s.upload)
	mux.HandleFunc("GET /v1/repos/{user}/{repo}/versions", s.versions)
	mux.HandleFunc("GET /v1/repos/{user}/{repo}/latest", s.latest)
	mux.HandleFunc("GET /v1/repos/{user}/{repo}/versions/{version}", s.version)
	return httptest.NewServer(mux)
}

func key(r *http.Request) string {
	return r.PathValue("user") + "/" + r.PathValue("repo")
}

func (s *stubServer) upload(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if want := r.Header.Get("X-Content-Sha256"); want != got {
		http.Error(w, `{"error":"hash mismatch"}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	k := key(r)
	s.store[k] = append(s.store[k], body)
	version := len(s.store[k])
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"version": version, "hash": got, "size": len(body),
		"timestamp": time.Now(), "filename": fmt.Sprintf("%08d.zip", version),
	})
}

func (s *stubServer) versions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(r)
	vers := s.store[k]
	if len(vers) == 0 {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	type vi struct {
		Version   int       `json:"version"`
		Hash      string    `json:"hash"`
		Size      int64     `json:"size"`
		Timestamp time.Time `json:"timestamp"`
		Filename  string    `json:"filename"`
	}
	out := struct {
		User     string `json:"user"`
		Repo     string `json:"repo"`
		Versions []vi   `json:"versions"`
	}{User: r.PathValue("user"), Repo: r.PathValue("repo")}
	for i, b := range vers {
		sum := sha256.Sum256(b)
		out.Versions = append(out.Versions, vi{
			Version: i + 1, Hash: hex.EncodeToString(sum[:]), Size: int64(len(b)),
			Timestamp: time.Now(), Filename: fmt.Sprintf("%08d.zip", i+1),
		})
	}
	sort.Slice(out.Versions, func(a, b int) bool { return out.Versions[a].Version < out.Versions[b].Version })
	json.NewEncoder(w).Encode(out)
}

func (s *stubServer) serveVersion(w http.ResponseWriter, r *http.Request, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vers := s.store[key(r)]
	if version < 1 || version > len(vers) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	b := vers[version-1]
	sum := sha256.Sum256(b)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Content-Sha256", hex.EncodeToString(sum[:]))
	w.Header().Set("X-Version", strconv.Itoa(version))
	w.Write(b)
}

func (s *stubServer) latest(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.store[key(r)])
	s.mu.Unlock()
	s.serveVersion(w, r, n)
}

func (s *stubServer) version(w http.ResponseWriter, r *http.Request) {
	v, _ := strconv.Atoi(r.PathValue("version"))
	s.serveVersion(w, r, v)
}

func TestEngineUploadDownloadRoundTrip(t *testing.T) {
	srv := newStubServer()
	defer srv.Close()

	// Config in a temp dir.
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	store, err := config.Open(cfgPath)
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if err := store.Update(func(c *config.Config) { c.ServerURL = srv.URL }); err != nil {
		t.Fatalf("set server url: %v", err)
	}

	// Save folder with content.
	folder := filepath.Join(t.TempDir(), "saves")
	if err := os.MkdirAll(filepath.Join(folder, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "save.dat"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "sub", "b.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := store.AddRepo(config.RepoConfig{
		Name: "test", User: "alice", Repo: "game", Folder: folder, Enabled: true,
	})
	if err != nil {
		t.Fatalf("add repo: %v", err)
	}

	eng := New(store)

	// Upload creates version 1.
	if err := eng.UploadNow(repo.ID); err != nil {
		t.Fatalf("upload: %v", err)
	}
	idx, err := eng.ListVersions(repo.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(idx.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(idx.Versions))
	}

	// Wipe the folder, then restore latest and verify contents came back.
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	if err := eng.DownloadVersion(repo.ID, 0); err != nil {
		t.Fatalf("download: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(folder, "save.dat"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("restored content = %q, want %q", got, "hello world")
	}
	gotNested, err := os.ReadFile(filepath.Join(folder, "sub", "b.txt"))
	if err != nil {
		t.Fatalf("read restored nested file: %v", err)
	}
	if string(gotNested) != "nested" {
		t.Fatalf("restored nested = %q, want %q", gotNested, "nested")
	}
}
