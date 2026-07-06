package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"sdvc/server/internal/server"
	"sdvc/server/internal/store"
)

func main() {
	addr := getenv("SDVC_ADDR", ":8080")
	dataDir := getenv("SDVC_DATA", "data")
	maxUpload := getenvInt64("SDVC_MAX_UPLOAD_BYTES", 1<<30) // 1 GiB default

	st, err := store.New(dataDir)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}

	srv := server.New(st, server.Options{MaxUploadBytes: maxUpload})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	log.Printf("sdvc server listening on %s (data dir: %q, max upload: %d bytes)", addr, dataDir, maxUpload)
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
