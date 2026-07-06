// Package web serves the local configuration UI and its JSON API.
package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"strings"

	"sdvc/client/internal/config"
	"sdvc/client/internal/engine"
)

//go:embed index.html
var content embed.FS

// Server exposes the local web UI.
type Server struct {
	store  *config.Store
	engine *engine.Engine
}

// New creates the web server.
func New(store *config.Store, eng *engine.Engine) *Server {
	return &Server{store: store, engine: eng}
}

// Handler returns the configured HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/repos", s.handleListRepos)
	mux.HandleFunc("POST /api/repos", s.handleAddRepo)
	mux.HandleFunc("PUT /api/repos/{id}", s.handleUpdateRepo)
	mux.HandleFunc("DELETE /api/repos/{id}", s.handleDeleteRepo)
	mux.HandleFunc("POST /api/repos/{id}/upload", s.handleUploadNow)
	mux.HandleFunc("GET /api/repos/{id}/versions", s.handleRepoVersions)
	mux.HandleFunc("POST /api/repos/{id}/download", s.handleDownload)

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := content.ReadFile("index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

type settingsDTO struct {
	ServerURL           string `json:"serverURL"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
	WebPort             int    `json:"webPort"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	writeJSON(w, http.StatusOK, settingsDTO{
		ServerURL:           cfg.ServerURL,
		PollIntervalSeconds: cfg.PollIntervalSeconds,
		WebPort:             cfg.WebPort,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	err := s.store.Update(func(cfg *config.Config) {
		if in.ServerURL != "" {
			cfg.ServerURL = in.ServerURL
		}
		if in.PollIntervalSeconds > 0 {
			cfg.PollIntervalSeconds = in.PollIntervalSeconds
		}
		if in.WebPort > 0 {
			cfg.WebPort = in.WebPort
		}
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleGetSettings(w, r)
}

// repoDTO combines persisted config with live status for the UI.
type repoDTO struct {
	config.RepoConfig
	Status engine.RepoStatus `json:"status"`
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	out := make([]repoDTO, 0, len(cfg.Repos))
	for _, rc := range cfg.Repos {
		out = append(out, repoDTO{RepoConfig: rc, Status: s.engine.Status(rc.ID)})
	}
	writeJSON(w, http.StatusOK, out)
}

type repoInput struct {
	Name      string   `json:"name"`
	User      string   `json:"user"`
	Repo      string   `json:"repo"`
	Folder    string   `json:"folder"`
	Processes []string `json:"processes"`
	Enabled   bool     `json:"enabled"`
}

func (s *Server) handleAddRepo(w http.ResponseWriter, r *http.Request) {
	var in repoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if in.User == "" || in.Repo == "" || in.Folder == "" {
		writeError(w, http.StatusBadRequest, "user, repo and folder are required")
		return
	}
	name := in.Name
	if name == "" {
		name = in.User + "/" + in.Repo
	}
	created, err := s.store.AddRepo(config.RepoConfig{
		Name:      name,
		User:      in.User,
		Repo:      in.Repo,
		Folder:    in.Folder,
		Processes: cleanStrings(in.Processes),
		Enabled:   in.Enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateRepo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in repoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ok, err := s.store.UpdateRepo(id, func(rc *config.RepoConfig) {
		if in.Name != "" {
			rc.Name = in.Name
		}
		if in.User != "" {
			rc.User = in.User
		}
		if in.Repo != "" {
			rc.Repo = in.Repo
		}
		if in.Folder != "" {
			rc.Folder = in.Folder
		}
		rc.Processes = cleanStrings(in.Processes)
		rc.Enabled = in.Enabled
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok, err := s.store.RemoveRepo(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleUploadNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.engine.UploadNow(id); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "uploaded"})
}

func (s *Server) handleRepoVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idx, err := s.engine.ListVersions(id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Version int `json:"version"` // 0 = latest
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	if err := s.engine.DownloadVersion(id, in.Version); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "downloaded"})
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
