// Command browserg is the gateway in front of the browser pool (browserd): it exposes /v1/browser and an aggregated
// /v1/capabilities to nexus, routes a session stickily to the instance that holds its Chromium, and enforces the
// per-tenant cap across the whole pool.
//
// Why it is a separate service rather than a second pool inside sandboxg: the browser channel and the sandbox channels
// answer different questions. The browser's capacity unit is a live Chromium (300 MB, one per session, state that cannot be
// migrated), its stickiness rule is "every open with a session key", and its per-tenant cap has to be counted globally.
// Folding that into sandboxg would give one process two load models, two capabilities shapes and two sticky namespaces —
// and one restart that drops every browser connection along with every exec route.
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

	"flame/browserg/internal/gw"
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

	if err := xlog.Init(cfg.LogDir, "browserg"); err != nil {
		fmt.Fprintln(os.Stderr, "browserg: init log:", err)
		os.Exit(1)
	}

	if cfg.Token == "" {
		xlog.Error("browserg: token is empty - no authentication on /v1 endpoints")
	}
	if cfg.Redis.Addr == "" {
		xlog.Error("browserg: redis is not configured - sticky bindings live in this process only; " +
			"a deployment with several gateway instances must share redis, otherwise one session can be routed to " +
			"different pool instances and end up with two Chromiums")
	}
	if cfg.MaxPerTenant > 0 {
		xlog.Info("browserg: max_per_tenant=%d is enforced here; leave browserd's own max_per_tenant at 0 "+
			"(a per-instance cap multiplies by the number of instances)", cfg.MaxPerTenant)
	}

	gateway, err := gw.New(cfg)
	if err != nil {
		xlog.Error("browserg: %v", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.RefreshLoop(ctx)
	go gateway.HealthLoop(ctx)
	go gateway.DiscoveryLoop(ctx)

	xlog.Info("browserg: listening on %s, %d instance(s)", cfg.Listen, len(cfg.Backends))

	httpSrv := &http.Server{Addr: cfg.Listen, Handler: gateway.Router()}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			xlog.Error("browserg: server exited: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	xlog.Info("browserg: signal received, shutting down ...")
	cancel()
	_ = httpSrv.Close()
	xlog.Info("browserg: bye")
}
