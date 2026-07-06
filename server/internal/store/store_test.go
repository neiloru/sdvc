package store

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSaveVerifyAndVersioning(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// First upload.
	body1 := "save-data-v1"
	v1, err := st.Save("alice", "game", strings.NewReader(body1), hashOf(body1))
	if err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	if v1.Version != 1 {
		t.Fatalf("expected version 1, got %d", v1.Version)
	}

	// Second upload increments version and never overwrites.
	body2 := "save-data-v2"
	v2, err := st.Save("alice", "game", strings.NewReader(body2), hashOf(body2))
	if err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("expected version 2, got %d", v2.Version)
	}

	// Hash mismatch is rejected.
	if _, err := st.Save("alice", "game", strings.NewReader("tampered"), hashOf("original")); err == nil {
		t.Fatal("expected hash mismatch error, got nil")
	}

	// Missing hash is rejected.
	if _, err := st.Save("alice", "game", strings.NewReader("x"), ""); err == nil {
		t.Fatal("expected hash-required error, got nil")
	}

	// Path traversal is rejected.
	if _, err := st.Save("../evil", "game", strings.NewReader("x"), hashOf("x")); err == nil {
		t.Fatal("expected invalid-name error, got nil")
	}

	// Latest returns v2.
	latest, err := st.Latest("alice", "game")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected latest version 2, got %d", latest.Version)
	}

	// Download v1 returns original content.
	f, info, err := st.Open("alice", "game", 1)
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read v1: %v", err)
	}
	if string(got) != body1 {
		t.Fatalf("v1 content = %q, want %q", got, body1)
	}
	if info.Hash != hashOf(body1) {
		t.Fatalf("v1 hash = %s, want %s", info.Hash, hashOf(body1))
	}
}
