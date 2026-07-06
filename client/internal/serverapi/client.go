// Package serverapi is a typed HTTP client for the sdvc server.
package serverapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound indicates the repo or version does not exist on the server.
var ErrNotFound = errors.New("not found")

// VersionInfo mirrors the server's version metadata.
type VersionInfo struct {
	Version   int       `json:"version"`
	Hash      string    `json:"hash"`
	Size      int64     `json:"size"`
	Timestamp time.Time `json:"timestamp"`
	Filename  string    `json:"filename"`
}

// RepoIndex mirrors the server's repo index.
type RepoIndex struct {
	User     string        `json:"user"`
	Repo     string        `json:"repo"`
	Versions []VersionInfo `json:"versions"`
}

// Latest returns the newest version, or (VersionInfo{}, false) if empty.
func (idx RepoIndex) Latest() (VersionInfo, bool) {
	if len(idx.Versions) == 0 {
		return VersionInfo{}, false
	}
	best := idx.Versions[0]
	for _, v := range idx.Versions[1:] {
		if v.Version > best.Version {
			best = v
		}
	}
	return best, true
}

// Client talks to an sdvc server.
type Client struct {
	base string
	http *http.Client
}

// New creates a client for the given base URL (e.g. "http://localhost:8080").
func New(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 0}, // no timeout: uploads/downloads may be large
	}
}

// Upload posts a zip file with its expected SHA-256 and returns the created version.
func (c *Client) Upload(user, repo, zipPath, hash string) (VersionInfo, error) {
	f, err := os.Open(zipPath)
	if err != nil {
		return VersionInfo{}, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return VersionInfo{}, err
	}

	url := fmt.Sprintf("%s/v1/repos/%s/%s", c.base, user, repo)
	req, err := http.NewRequest(http.MethodPost, url, f)
	if err != nil {
		return VersionInfo{}, err
	}
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("X-Content-Sha256", hash)
	req.ContentLength = stat.Size()

	resp, err := c.http.Do(req)
	if err != nil {
		return VersionInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return VersionInfo{}, httpError("upload", resp)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return VersionInfo{}, fmt.Errorf("decode upload response: %w", err)
	}
	return info, nil
}

// ListVersions returns all versions. A missing repo yields an empty index (no error).
func (c *Client) ListVersions(user, repo string) (RepoIndex, error) {
	url := fmt.Sprintf("%s/v1/repos/%s/%s/versions", c.base, user, repo)
	resp, err := c.http.Get(url)
	if err != nil {
		return RepoIndex{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return RepoIndex{User: user, Repo: repo}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return RepoIndex{}, httpError("list versions", resp)
	}
	var idx RepoIndex
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return RepoIndex{}, fmt.Errorf("decode versions: %w", err)
	}
	return idx, nil
}

// Download fetches a version (version <= 0 means latest) into destZip, verifying
// the SHA-256 reported by the server against the received bytes.
func (c *Client) Download(user, repo string, version int, destZip string) (VersionInfo, error) {
	var url string
	if version <= 0 {
		url = fmt.Sprintf("%s/v1/repos/%s/%s/latest", c.base, user, repo)
	} else {
		url = fmt.Sprintf("%s/v1/repos/%s/%s/versions/%d", c.base, user, repo, version)
	}

	resp, err := c.http.Get(url)
	if err != nil {
		return VersionInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return VersionInfo{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return VersionInfo{}, httpError("download", resp)
	}

	expectedHash := strings.ToLower(resp.Header.Get("X-Content-Sha256"))
	serverVersion, _ := strconv.Atoi(resp.Header.Get("X-Version"))

	out, err := os.Create(destZip)
	if err != nil {
		return VersionInfo{}, err
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hasher), resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(destZip)
		return VersionInfo{}, copyErr
	}
	if closeErr != nil {
		os.Remove(destZip)
		return VersionInfo{}, closeErr
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if expectedHash != "" && actualHash != expectedHash {
		os.Remove(destZip)
		return VersionInfo{}, fmt.Errorf("hash mismatch: server reported %s, got %s", expectedHash, actualHash)
	}

	return VersionInfo{
		Version:   serverVersion,
		Hash:      actualHash,
		Size:      size,
		Timestamp: time.Now().UTC(),
	}, nil
}

func httpError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	// Try to extract {"error": "..."} for a cleaner message.
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
		msg = parsed.Error
	}
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("%s failed (%d): %s", action, resp.StatusCode, msg)
}
