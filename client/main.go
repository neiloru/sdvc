// Command sdvc-client is the cross-platform save-data version control client.
//
// It runs a local web UI (bound to 127.0.0.1) for configuration and a background
// sync engine that uploads local changes and downloads new versions from the
// server while the configured game/process is not running. A system-tray / macOS
// menu-bar item provides quick access and a clean way to quit.
//
// Works on Windows, macOS, Linux and SteamOS. Set SDVC_NO_TRAY=1 to run headless
// (no tray) in environments without a system tray host.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"sdvc/client/internal/config"
	"sdvc/client/internal/engine"
	"sdvc/client/internal/hidden"
	"sdvc/client/internal/notify"
	"sdvc/client/internal/tray"
	"sdvc/client/internal/web"
)

func main() {
	store, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfgPath, _ := config.Path()
	log.Printf("config: %s", cfgPath)

	cfg := store.Get()
	eng := engine.New(store)
	eng.SetNotifier(func(title, message string) { notify.Send(title, message) })

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go eng.Run(ctx)

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.WebPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           web.New(store, eng).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := "http://" + addr

	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		})

	}

	// Web server.
	go func() {
		log.Printf("sdvc client UI: %s", url)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("web server error: %v", err)
		}
	}()

	// Shut everything down when the context is cancelled (signal or Quit).
	go func() {
		<-ctx.Done()
		shutdown()
		tray.Quit()
	}()

	if os.Getenv("SDVC_NO_TRAY") == "1" {
		log.Printf("running headless (SDVC_NO_TRAY=1)")
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		return
	}

	// Tray runs on the main goroutine and blocks until Quit.
	tray.Run(tray.Options{
		Title:   "sdvc",
		Tooltip: "sdvc - save data version control",
		OnOpen:  func() { openBrowser(url) },
		OnQuit: func() {
			stop()     // cancel context
			shutdown() // stop web server
		},
	})
	log.Printf("shutting down")
}

// openBrowser best-effort opens the UI in the default browser.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd, args = "open", []string{url}
	default: // linux, steamos, others
		cmd, args = "xdg-open", []string{url}
	}
	c := exec.Command(cmd, args...)
	hidden.Apply(c)
	_ = c.Start()
}
