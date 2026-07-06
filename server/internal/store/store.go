// Package store provides on-disk storage for versioned save-data archives.
//
// Layout on disk:
//
//	<root>/<user>/<repo>/index.json      -> metadata for all versions
//	<root>/<user>/<repo>/00000001.zip    -> version 1 archive
//	<root>/<user>/<repo>/00000002.zip    -> version 2 archive
//	...
//
// Archives are never overwritten or deleted. Each upload creates a new,
// monotonically increasing version.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalidName is returned when a user or repo name is unsafe.
	ErrInvalidName = errors.New("invalid name")
	// ErrHashMismatch is returned when the uploaded content does not match the expected hash.
	ErrHashMismatch = errors.New("hash mismatch")
	// ErrHashRequired is returned when no expected hash was supplied.
	ErrHashRequired = errors.New("hash required")
	// ErrNotFound is returned when a repo or version does not exist.
	ErrNotFound = errors.New("not found")
	// ErrEmptyUpload is returned when the uploaded body contains no data.
	ErrEmptyUpload = errors.New("empty upload")
)

// namePattern restricts user/repo names to a safe subset to prevent path traversal.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// VersionInfo describes a single stored archive.
type VersionInfo struct {
	Version   int       `json:"version"`
	Hash      string    `json:"hash"` // lowercase hex SHA-256
	Size      int64     `json:"size"`
	Timestamp time.Time `json:"timestamp"`
	Filename  string    `json:"filename"`
}

// RepoIndex is the on-disk metadata for a single user/repo.
type RepoIndex struct {
	User     string        `json:"user"`
	Repo     string        `json:"repo"`
	Versions []VersionInfo `json:"versions"`
}

// RepoRef identifies a repository.
type RepoRef struct {
	User string `json:"user"`
	Repo string `json:"repo"`
}

// Store is a concurrency-safe archive store rooted at a directory.
type Store struct {
	root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-repo serialization for uploads/index writes
}

// New creates a Store rooted at the given directory, creating it if needed.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("store: root must not be empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("store: create root: %w", err)
	}
	return &Store{root: root, locks: make(map[string]*sync.Mutex)}, nil
}

// validName reports whether a user/repo name is safe to use as a path segment.
func validName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return namePattern.MatchString(name)
}

func (s *Store) repoLock(user, repo string) *sync.Mutex {
	key := user + "/" + repo
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	return l
}

func (s *Store) repoDir(user, repo string) string {
	return filepath.Join(s.root, user, repo)
}

func (s *Store) indexPath(user, repo string) string {
	return filepath.Join(s.repoDir(user, repo), "index.json")
}

// loadIndex reads the repo index. A missing index yields an empty (non-error) index.
func (s *Store) loadIndex(user, repo string) (RepoIndex, error) {
	idx := RepoIndex{User: user, Repo: repo}
	data, err := os.ReadFile(s.indexPath(user, repo))
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return idx, fmt.Errorf("read index: %w", err)
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, fmt.Errorf("parse index: %w", err)
	}
	return idx, nil
}

// saveIndex atomically writes the repo index.
func (s *Store) saveIndex(user, repo string, idx RepoIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	dir := s.repoDir(user, repo)
	tmp, err := os.CreateTemp(dir, "index-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp index: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Rename(tmpName, s.indexPath(user, repo)); err != nil {
		return fmt.Errorf("rename index: %w", err)
	}
	return nil
}

// Save stores a new archive version from r, verifying its SHA-256 against
// expectedHash (lowercase or uppercase hex). It never overwrites an existing
// archive. On hash mismatch the partially written temp file is discarded and
// ErrHashMismatch is returned.
func (s *Store) Save(user, repo string, r io.Reader, expectedHash string) (VersionInfo, error) {
	if !validName(user) || !validName(repo) {
		return VersionInfo{}, ErrInvalidName
	}
	expectedHash = strings.ToLower(strings.TrimSpace(expectedHash))
	if expectedHash == "" {
		return VersionInfo{}, ErrHashRequired
	}
	if _, err := hex.DecodeString(expectedHash); err != nil || len(expectedHash) != 64 {
		return VersionInfo{}, fmt.Errorf("%w: expected 64 hex chars", ErrHashMismatch)
	}

	lock := s.repoLock(user, repo)
	lock.Lock()
	defer lock.Unlock()

	dir := s.repoDir(user, repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return VersionInfo{}, fmt.Errorf("create repo dir: %w", err)
	}

	// Stream the body to a temp file while computing the hash.
	tmp, err := os.CreateTemp(dir, "upload-*.tmp")
	if err != nil {
		return VersionInfo{}, fmt.Errorf("create temp upload: %w", err)
	}
	tmpName := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		tmp.Close()
		return VersionInfo{}, fmt.Errorf("write upload: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return VersionInfo{}, fmt.Errorf("sync upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return VersionInfo{}, fmt.Errorf("close upload: %w", err)
	}

	if size == 0 {
		return VersionInfo{}, ErrEmptyUpload
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != expectedHash {
		return VersionInfo{}, fmt.Errorf("%w: expected %s, got %s", ErrHashMismatch, expectedHash, actualHash)
	}

	idx, err := s.loadIndex(user, repo)
	if err != nil {
		return VersionInfo{}, err
	}

	nextVersion := 1
	for _, v := range idx.Versions {
		if v.Version >= nextVersion {
			nextVersion = v.Version + 1
		}
	}

	info := VersionInfo{
		Version:   nextVersion,
		Hash:      actualHash,
		Size:      size,
		Timestamp: time.Now().UTC(),
		Filename:  fmt.Sprintf("%08d.zip", nextVersion),
	}

	finalPath := filepath.Join(dir, info.Filename)
	if _, err := os.Stat(finalPath); err == nil {
		// Safety: never overwrite an existing archive.
		return VersionInfo{}, fmt.Errorf("archive %s already exists", info.Filename)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return VersionInfo{}, fmt.Errorf("commit archive: %w", err)
	}
	cleanupTmp = false // committed

	idx.Versions = append(idx.Versions, info)
	if err := s.saveIndex(user, repo, idx); err != nil {
		return VersionInfo{}, err
	}

	return info, nil
}

// ListVersions returns all versions for a repo, oldest first.
func (s *Store) ListVersions(user, repo string) (RepoIndex, error) {
	if !validName(user) || !validName(repo) {
		return RepoIndex{}, ErrInvalidName
	}
	if _, err := os.Stat(s.repoDir(user, repo)); errors.Is(err, os.ErrNotExist) {
		return RepoIndex{}, ErrNotFound
	}
	idx, err := s.loadIndex(user, repo)
	if err != nil {
		return RepoIndex{}, err
	}
	sort.Slice(idx.Versions, func(i, j int) bool {
		return idx.Versions[i].Version < idx.Versions[j].Version
	})
	return idx, nil
}

// Latest returns metadata for the newest version of a repo.
func (s *Store) Latest(user, repo string) (VersionInfo, error) {
	idx, err := s.ListVersions(user, repo)
	if err != nil {
		return VersionInfo{}, err
	}
	if len(idx.Versions) == 0 {
		return VersionInfo{}, ErrNotFound
	}
	return idx.Versions[len(idx.Versions)-1], nil
}

// Open returns a readable handle to a specific version's archive along with its metadata.
// The caller is responsible for closing the returned file.
func (s *Store) Open(user, repo string, version int) (*os.File, VersionInfo, error) {
	if !validName(user) || !validName(repo) {
		return nil, VersionInfo{}, ErrInvalidName
	}
	idx, err := s.ListVersions(user, repo)
	if err != nil {
		return nil, VersionInfo{}, err
	}
	var info VersionInfo
	found := false
	for _, v := range idx.Versions {
		if v.Version == version {
			info = v
			found = true
			break
		}
	}
	if !found {
		return nil, VersionInfo{}, ErrNotFound
	}
	f, err := os.Open(filepath.Join(s.repoDir(user, repo), info.Filename))
	if errors.Is(err, os.ErrNotExist) {
		return nil, VersionInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, VersionInfo{}, fmt.Errorf("open archive: %w", err)
	}
	return f, info, nil
}

// OpenLatest returns a readable handle to the newest version's archive.
func (s *Store) OpenLatest(user, repo string) (*os.File, VersionInfo, error) {
	info, err := s.Latest(user, repo)
	if err != nil {
		return nil, VersionInfo{}, err
	}
	return s.Open(user, repo, info.Version)
}

// ListRepos returns every repository owned by a single user.
func (s *Store) ListRepos(user string) ([]RepoRef, error) {
	if !validName(user) {
		return nil, ErrInvalidName
	}
	entries, err := os.ReadDir(filepath.Join(s.root, user))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read user dir: %w", err)
	}
	var refs []RepoRef
	for _, rp := range entries {
		if rp.IsDir() {
			refs = append(refs, RepoRef{User: user, Repo: rp.Name()})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Repo < refs[j].Repo
	})
	return refs, nil
}
