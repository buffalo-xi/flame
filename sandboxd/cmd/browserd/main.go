// Command browserd is the standalone browser pool: one Chromium per live browser session, a hard cap on how many may be
// live at once, a queue for the rest, and idle reclamation.
//
// It is deliberately independent of sandboxd: the sandbox mode (none / nsjail / docker) decides how code runs, while this
// service decides how a browser runs — which is the only way a sandbox that cannot host Chromium can still offer the
// browser tool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flame/sandboxd/internal/browserpool"
	"flame/tools/xlog"
)

func main() {
	cfgPath := flag.String("config", "browserd.yaml", "path to the configuration file")
	flag.Parse()

	cfg, err := browserpool.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := xlog.Init(cfg.LogDir, "browserd"); err != nil {
		fmt.Fprintln(os.Stderr, "browserd: init log:", err)
		os.Exit(1)
	}
	defer xlog.Close()

	xlog.Info("browserd: mode=%s workers=%d max_per_tenant=%d queue_timeout=%ds idle=%ds janitor=%ds",
		cfg.Mode, cfg.Workers, cfg.MaxPerTenant, cfg.QueueTimeoutSec, cfg.IdleTimeoutSec, cfg.JanitorSec)
	// The two directory roots are resolved against the startup cwd, so print what they resolved to: a wrong profile root
	// otherwise shows up as "the disk filled up", not as a configuration error.
	xlog.Info("browserd: driver=%s exec_path=%q profile_root=%s downloads_root=%s",
		cfg.Browser.Binary, cfg.Browser.ExecPath, cfg.Browser.ProfileRoot, cfg.Browser.DownloadsRoot)

	srv := browserpool.NewServer(cfg)
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		xlog.Info("browserd: listening on %s", cfg.Listen)
		if lerr := httpSrv.ListenAndServe(); lerr != nil && !errors.Is(lerr, http.ErrServerClosed) {
			xlog.Error("browserd: server exited: %v", lerr)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	xlog.Info("browserd: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
