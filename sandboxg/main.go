// Command sandboxg is the gateway in front of sandboxd: it exposes the same /v1/* protocol to nexus as
// sandboxd does, and load-balances the backends (exec/code) while routing sticky (interactive, by
// session_id).
//
// The protocol is transparent: frames are not rewritten, it only "reads the open frame → picks a backend
// → pipes both directions". In a multi-instance deployment the sticky binding lives in redis (shared), so
// the same session lands on the same sandboxd as far as possible.
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

	"flame/sandboxg/internal/gw"
	"flame/tools/xlog"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the configuration file")
	flag.Parse()

	cfg, err := gw.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Initialize logging (xlog must be Init'ed first, otherwise Info/Error panic).
	if err := xlog.Init(cfg.LogDir, "sandboxg"); err != nil {
		fmt.Fprintln(os.Stderr, "sandboxg: init log:", err)
		os.Exit(1)
	}

	if cfg.Token == "" {
		xlog.Error("sandboxg: token is empty — no authentication on /v1 endpoints")
	}

	gateway, err := gw.New(cfg)
	if err != nil {
		xlog.Error("sandboxg: %v", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.RefreshLoop(ctx)
	go gateway.HealthLoop(ctx)
	go gateway.DiscoveryLoop(ctx)

	xlog.Info("sandboxg: listening on %s, %d backends", cfg.Listen, len(cfg.Backends))

	httpSrv := &http.Server{Addr: cfg.Listen, Handler: gateway.Router()}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			xlog.Error("sandboxg: server exited: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	xlog.Info("sandboxg: signal received, shutting down ...")
	cancel()
	_ = httpSrv.Close()
	xlog.Info("sandboxg: bye")
}
