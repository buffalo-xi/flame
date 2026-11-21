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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"flame/browserd/internal/browserpool"
	"flame/tools/xlog"
	"flame/tools/xregistry"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to the configuration file")
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
	xlog.Info("browserd: driver=%s exec_path=%q profile_root=%s workspace_root=%s",
		cfg.Browser.Binary, cfg.Browser.ExecPath, cfg.Browser.ProfileRoot, cfg.WorkspaceRoot)

	// A wrong workspace_root is silent by nature: this service would create its own empty tree, the browser would write
	// into it happily, and the model would only notice when the screenshot "is not there". tenants/ is the one marker
	// nexus always creates under its storage root, so its absence is worth a loud line (harmless only on a fresh
	// deployment where no tenant exists yet).
	if _, err := os.Stat(filepath.Join(cfg.WorkspaceRoot, "tenants")); err != nil {
		xlog.Error("browserd: %s has no tenants/ subdirectory - workspace_root must be the same directory as nexus's "+
			"storage.root_dir, otherwise screenshots and downloads land where no tenant can read them "+
			"(ignore this only before the first tenant exists)", cfg.WorkspaceRoot)
	}

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

	// Service registration (optional): announce this instance so browserg's discovery can find it. Done after HTTP is up,
	// so the registry's /healthz probe succeeds on the first try.
	var registrar xregistry.Registrar
	if cfg.Registry.Enabled {
		rc := registryConfig(cfg)
		reg, rerr := xregistry.NewRegistrar(rc)
		if rerr != nil {
			xlog.Error("browserd: create registrar: %v", rerr)
		} else {
			registrar = reg
			if rerr := reg.Register(); rerr != nil {
				xlog.Error("browserd: register: %v (will retry via KeepRegistered)", rerr)
			} else {
				xlog.Info("browserd: registered to %s as service %s", rc.Type, rc.ServiceName)
			}
			// Self-healing keepalive: both an eviction (registry disconnect) and an initial failure heal themselves.
			reg.KeepRegistered(ctx, 30*time.Second)
		}
	}

	<-ctx.Done()
	xlog.Info("browserd: shutting down")
	if registrar != nil {
		_ = registrar.Deregister()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

// registryConfig turns the config into xregistry's registrar config.
//
// ServiceAddr must be an address browserg can reach (listen is often 127.0.0.1, useless across machines); ServicePort
// defaults to the one parsed out of listen.
func registryConfig(cfg *browserpool.Config) *xregistry.RegistrarConfig {
	name := cfg.Registry.ServiceName
	if name == "" {
		name = "browserd"
	}
	path := cfg.Registry.HealthCheckPath
	if path == "" {
		path = "/healthz"
	}
	port := cfg.Registry.ServicePort
	if port == 0 {
		if _, p, err := net.SplitHostPort(cfg.Listen); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	return &xregistry.RegistrarConfig{
		Type:            xregistry.Type(cfg.Registry.Type),
		Address:         cfg.Registry.Address,
		ServiceName:     name,
		ServiceAddr:     cfg.Registry.ServiceAddr,
		ServicePort:     port,
		HealthCheckPath: path,
	}
}
