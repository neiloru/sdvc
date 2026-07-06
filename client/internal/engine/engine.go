// Package engine runs the background sync loop and exposes manual actions.
package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"sdvc/client/internal/config"
	"sdvc/client/internal/proc"
	"sdvc/client/internal/scan"
	"sdvc/client/internal/serverapi"
)

// RepoStatus is live, non-persisted status for a repo shown in the UI.
type RepoStatus struct {
	ProcessRunning      bool      `json:"processRunning"`
	Busy                bool      `json:"busy"`
	Message             string    `json:"message"`
	LatestServerVersion int       `json:"latestServerVersion"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// Engine coordinates change detection, uploads and downloads.
type Engine struct {
	store *config.Store

	mu       sync.Mutex
	locks    map[string]*sync.Mutex // per-repo op serialization
	statuses map[string]RepoStatus
	notify   func(title, message string)
}

// SetNotifier sets a callback used to surface desktop notifications.
func (e *Engine) SetNotifier(fn func(title, message string)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notify = fn
}

// notifyf sends a desktop notification if a notifier is configured.
func (e *Engine) notifyf(title, message string) {
	e.mu.Lock()
	fn := e.notify
	e.mu.Unlock()
	if fn != nil {
		fn(title, message)
	}
}

// New creates an Engine.
func New(store *config.Store) *Engine {
	return &Engine{
		store:    store,
		locks:    make(map[string]*sync.Mutex),
		statuses: make(map[string]RepoStatus),
	}
}

// Run drives the periodic sync loop until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	// Initial pass shortly after start.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			e.syncAll(ctx)
			interval := time.Duration(e.store.Get().PollIntervalSeconds) * time.Second
			if interval < 5*time.Second {
				interval = 5 * time.Second
			}
			timer.Reset(interval)
		}
	}
}

// Status returns the live status for a repo.
func (e *Engine) Status(id string) RepoStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statuses[id]
}

func (e *Engine) setStatus(id string, fn func(s *RepoStatus)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.statuses[id]
	fn(&s)
	s.UpdatedAt = time.Now()
	e.statuses[id] = s
}

func (e *Engine) repoLock(id string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.locks[id]
	if !ok {
		l = &sync.Mutex{}
		e.locks[id] = l
	}
	return l
}

func (e *Engine) syncAll(ctx context.Context) {
	cfg := e.store.Get()
	for _, rc := range cfg.Repos {
		if ctx.Err() != nil {
			return
		}
		if !rc.Enabled {
			e.setStatus(rc.ID, func(s *RepoStatus) { s.Message = "disabled" })
			continue
		}
		if err := e.syncRepo(cfg.ServerURL, rc); err != nil {
			log.Printf("[%s] sync error: %v", rc.Name, err)
		}
	}
}

// syncRepo performs one auto-sync pass for a repo. It never touches files while
// a configured process is running.
func (e *Engine) syncRepo(serverURL string, rc config.RepoConfig) error {
	lock := e.repoLock(rc.ID)
	lock.Lock()
	defer lock.Unlock()

	// Re-read the latest persisted state for this repo.
	rc, ok := e.currentRepo(rc.ID)
	if !ok {
		return nil
	}

	running, err := proc.AnyRunning(rc.Processes)
	if err != nil {
		e.setStatus(rc.ID, func(s *RepoStatus) { s.Message = "cannot check processes: " + err.Error() })
		return err
	}
	e.setStatus(rc.ID, func(s *RepoStatus) { s.ProcessRunning = running })
	if running {
		e.setStatus(rc.ID, func(s *RepoStatus) { s.Message = "waiting: game is running" })
		return nil
	}

	api := serverapi.New(serverURL)
	idx, err := api.ListVersions(rc.User, rc.Repo)
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}
	latest, hasServer := idx.Latest()
	latestVersion := 0
	if hasServer {
		latestVersion = latest.Version
	}
	e.setStatus(rc.ID, func(s *RepoStatus) { s.LatestServerVersion = latestVersion })

	current, err := scan.ContentHash(rc.Folder)
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}
	localChanged := current != "" && current != rc.LastSyncedContentHash
	serverNewer := latestVersion > rc.LastSyncedVersion

	switch {
	case localChanged:
		return e.doUpload(api, rc, current)
	case serverNewer && hasServer:
		return e.doDownload(api, rc, latest.Version)
	default:
		e.setStatus(rc.ID, func(s *RepoStatus) {
			s.Busy = false
			s.Message = "up to date"
		})
		return nil
	}
}

func (e *Engine) doUpload(api *serverapi.Client, rc config.RepoConfig, contentHash string) error {
	e.setStatus(rc.ID, func(s *RepoStatus) { s.Busy = true; s.Message = "uploading changes" })
	defer e.setStatus(rc.ID, func(s *RepoStatus) { s.Busy = false })

	zipPath, zipHash, err := scan.CreateZip(rc.Folder)
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}
	defer os.Remove(zipPath)

	info, err := api.Upload(rc.User, rc.Repo, zipPath, zipHash)
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}

	e.store.UpdateRepo(rc.ID, func(r *config.RepoConfig) {
		r.LastSyncedContentHash = contentHash
		r.LastSyncedVersion = info.Version
		r.LastSyncTime = time.Now()
		r.LastError = ""
	})
	e.setStatus(rc.ID, func(s *RepoStatus) {
		s.Message = fmt.Sprintf("uploaded version %d", info.Version)
		s.LatestServerVersion = info.Version
	})
	log.Printf("[%s] uploaded version %d", rc.Name, info.Version)
	return nil
}

func (e *Engine) doDownload(api *serverapi.Client, rc config.RepoConfig, version int) error {
	e.setStatus(rc.ID, func(s *RepoStatus) { s.Busy = true; s.Message = "downloading latest" })
	defer e.setStatus(rc.ID, func(s *RepoStatus) { s.Busy = false })

	tmpZip, err := os.CreateTemp("", "sdvc-download-*.zip")
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}
	tmpPath := tmpZip.Name()
	tmpZip.Close()
	defer os.Remove(tmpPath)

	info, err := api.Download(rc.User, rc.Repo, version, tmpPath)
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}
	if err := scan.ExtractReplace(rc.Folder, tmpPath); err != nil {
		e.recordError(rc.ID, err)
		return err
	}

	newHash, err := scan.ContentHash(rc.Folder)
	if err != nil {
		e.recordError(rc.ID, err)
		return err
	}

	e.store.UpdateRepo(rc.ID, func(r *config.RepoConfig) {
		r.LastSyncedContentHash = newHash
		r.LastSyncedVersion = info.Version
		r.LastSyncTime = time.Now()
		r.LastError = ""
	})
	e.setStatus(rc.ID, func(s *RepoStatus) {
		s.Message = fmt.Sprintf("downloaded version %d", info.Version)
	})
	log.Printf("[%s] downloaded version %d", rc.Name, info.Version)
	return nil
}

// UploadNow forces an immediate upload if the game is not running.
func (e *Engine) UploadNow(id string) error {
	lock := e.repoLock(id)
	lock.Lock()
	defer lock.Unlock()

	rc, ok := e.currentRepo(id)
	if !ok {
		return fmt.Errorf("repo not found")
	}
	running, err := proc.AnyRunning(rc.Processes)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("cannot upload while a configured process is running")
	}
	current, err := scan.ContentHash(rc.Folder)
	if err != nil {
		return err
	}
	if current == "" {
		return fmt.Errorf("save folder is empty or missing: %s", rc.Folder)
	}
	api := serverapi.New(e.store.Get().ServerURL)
	return e.doUpload(api, rc, current)
}

// DownloadVersion downloads a specific version (0 = latest) and restores it
// locally. It suppresses an immediate auto re-download by acknowledging the
// current latest server version.
func (e *Engine) DownloadVersion(id string, version int) error {
	lock := e.repoLock(id)
	lock.Lock()
	defer lock.Unlock()

	rc, ok := e.currentRepo(id)
	if !ok {
		return fmt.Errorf("repo not found")
	}
	running, err := proc.AnyRunning(rc.Processes)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("cannot download while a configured process is running")
	}

	api := serverapi.New(e.store.Get().ServerURL)
	idx, err := api.ListVersions(rc.User, rc.Repo)
	if err != nil {
		return err
	}
	latestVersion := 0
	if latest, ok := idx.Latest(); ok {
		latestVersion = latest.Version
	}

	tmpZip, err := os.CreateTemp("", "sdvc-download-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmpZip.Name()
	tmpZip.Close()
	defer os.Remove(tmpPath)

	info, err := api.Download(rc.User, rc.Repo, version, tmpPath)
	if err != nil {
		return err
	}
	if err := scan.ExtractReplace(rc.Folder, tmpPath); err != nil {
		return err
	}
	newHash, err := scan.ContentHash(rc.Folder)
	if err != nil {
		return err
	}

	ackVersion := latestVersion
	if info.Version > ackVersion {
		ackVersion = info.Version
	}
	e.store.UpdateRepo(rc.ID, func(r *config.RepoConfig) {
		r.LastSyncedContentHash = newHash
		r.LastSyncedVersion = ackVersion // avoid auto re-download of a newer version
		r.LastSyncTime = time.Now()
		r.LastError = ""
	})
	e.setStatus(rc.ID, func(s *RepoStatus) {
		s.Message = fmt.Sprintf("restored version %d", info.Version)
		s.LatestServerVersion = latestVersion
	})
	log.Printf("[%s] restored version %d", rc.Name, info.Version)
	return nil
}

// ListVersions returns the server-side version list for a repo.
func (e *Engine) ListVersions(id string) (serverapi.RepoIndex, error) {
	rc, ok := e.currentRepo(id)
	if !ok {
		return serverapi.RepoIndex{}, fmt.Errorf("repo not found")
	}
	api := serverapi.New(e.store.Get().ServerURL)
	return api.ListVersions(rc.User, rc.Repo)
}

func (e *Engine) currentRepo(id string) (config.RepoConfig, bool) {
	for _, rc := range e.store.Get().Repos {
		if rc.ID == id {
			return rc, true
		}
	}
	return config.RepoConfig{}, false
}

func (e *Engine) recordError(id string, err error) {
	e.store.UpdateRepo(id, func(r *config.RepoConfig) { r.LastError = err.Error() })
	e.setStatus(id, func(s *RepoStatus) { s.Busy = false; s.Message = "error: " + err.Error() })
}
