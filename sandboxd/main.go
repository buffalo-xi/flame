// Command sandboxd is the standalone sandbox service: it accepts execution requests and runs commands and
// scripts inside the configured isolation mode (none / nsjail / docker).
//
// Responsibility boundary:
//   - nexus does no isolation and holds no pool, it only submits simplex / duplex requests;
//   - the isolation mode, resource caps, process pool and concurrency quota are all decided by this
//     service's configuration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"flame/sandboxd/internal/daemon"
	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
	"flame/tools/xlog"
	"flame/tools/xregistry"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the configuration file")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := daemon.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Initialize logging (xlog's Logger must be Init'ed first, otherwise Info/Error panic).
	if err := xlog.Init(cfg.LogDir, "sandboxd"); err != nil {
		fmt.Fprintln(os.Stderr, "sandboxd: init log:", err)
		os.Exit(1)
	}

	// Config-time warnings (currently only legacy-key backfills): they can only be printed once xlog is ready.
	// Their only purpose is to be seen — a legacy key keeps working, but should be replaced when the config is
	// next edited.
	for _, w := range cfg.Warnings {
		xlog.Error("sandboxd: %s", w)
	}

	// Create the isolation implementation (which self-checks the nsjail/docker executable).
	iso, err := isolate.New(cfg.Mode, isolate.Options{
		Binary:         binaryFor(cfg),
		Rootfs:         cfg.Nsjail.Rootfs,
		UID:            cfg.Nsjail.UID,
		GID:            cfg.Nsjail.GID,
		DisableNetwork: cfg.Nsjail.DisableNetwork,
		ReadOnlyRootfs: cfg.Nsjail.ReadOnlyRootfs,
		CgroupVersion:  cfg.Nsjail.CgroupVersion,
		Image:          cfg.Docker.Image,
		Network:        cfg.Docker.Network,
		ReadOnly:       cfg.Docker.ReadOnly,
		User:           cfg.Docker.User,
		ExtraArgs:      extraArgsFor(cfg),
	})
	if err != nil {
		xlog.Error("sandboxd: init isolate: %v", err)
		os.Exit(1)
	}

	if len(cfg.AllowedMountRoots) == 0 {
		xlog.Error("sandboxd: allowed_mount_roots is empty — mount paths will NOT be validated")
	}
	if cfg.Token == "" {
		xlog.Error("sandboxd: token is empty — no authentication on /v1 endpoints")
	}
	if cfg.Mode == isolate.NoneName {
		xlog.Error("sandboxd: mode=none — commands run WITHOUT isolation")
	}
	// Network declaration: log the effective level next to the mode. "What the config says" and "what is
	// actually in effect" must be visible at a glance (the same approach as resolving the entry name per mode
	// for langs).
	xlog.Info("sandboxd: network policy mode=%s policy=%s", cfg.Mode, cfg.EffectiveNetwork())

	// The interactive hard cap: it is the real ceiling for script execution (a timeout sent with the request
	// can only tighten it, never relax it).
	// Without printing it, a case like nexus configuring script.<lang>.timeout=600 ("configured as if it did
	// nothing") could only be found by reading the code.
	xlog.Info("sandboxd: script timeout hard cap = %ds (code_max_sec); a request timeout can only lower it",
		cfg.CodeMaxSec)

	// Under mode=none commands are executed by the host and must resolve there — validate at startup, so the
	// failure does not surface on the first script call (other modes resolve commands inside the sandbox, so
	// this check is skipped).
	if err := checkCommands(cfg); err != nil {
		xlog.Error("sandboxd: %v", err)
		fmt.Fprintln(os.Stderr, "sandboxd:", err)
		os.Exit(1)
	}

	// Reclaim units left behind by a previous abnormal exit: --rm only guarantees "delete when the container
	// exits", not "the container exits when the service does" — after a hard kill the leftover units keep
	// holding memory.
	if err := cleanOrphans(iso); err != nil {
		xlog.Error("sandboxd: clean orphan units: %v", err)
	}

	srv, err := daemon.NewServer(cfg, iso)
	if err != nil {
		xlog.Error("sandboxd: %v", err)
		fmt.Fprintln(os.Stderr, "sandboxd:", err)
		os.Exit(1)
	}
	defer srv.Close()

	xlog.Info("sandboxd: listening on %s (mode=%s)", cfg.Listen, cfg.Mode)

	// http.Server instead of gin's Run: Run builds its own server internally and blocks, so no handle is
	// available outside and a graceful shutdown on a signal is impossible.
	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Router()}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			xlog.Error("sandboxd: server exited: %v", err)
			os.Exit(1)
		}
	}()

	// Service registration (optional): register with consul/nacos so sandboxg can discover it. Done after
	// HTTP is up, so consul's /healthz probe succeeds on the first try.
	var registrar xregistry.Registrar
	if cfg.Registry.Enabled {
		rc := registryConfig(cfg)
		reg, err := xregistry.NewRegistrar(rc)
		if err != nil {
			xlog.Error("sandboxd: create registrar: %v", err)
		} else {
			registrar = reg
			if err := reg.Register(); err != nil {
				xlog.Error("sandboxd: register: %v (will retry via KeepRegistered)", err)
			} else {
				xlog.Info("sandboxd: registered to %s as service %s", rc.Type, rc.ServiceName)
			}
			// Self-healing keepalive: periodically confirm the registration state; both an eviction (registry
			// disconnect) and an initial failure heal themselves.
			reg.KeepRegistered(ctx, 30*time.Second)
		}
	}

	// Graceful shutdown — the necessary counterpart of L2.
	//
	// L3's foreground process (docker run --rm -i) exits when stdin closes and the container goes with it;
	// L2's units are long-lived docker run -d containers whose lifetime is independent of the client, so
	// without an explicit Close they leave N `sleep infinity` processes behind and only the next startup's
	// CleanOrphans saves the day — that is a crash backstop, not a normal shutdown path.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	cancel() // let background loops such as KeepRegistered exit
	xlog.Info("sandboxd: signal received, closing units ...")
	if registrar != nil {
		_ = registrar.Deregister()
	}
	srv.Close()
	_ = httpSrv.Close()
	xlog.Info("sandboxd: bye")
}

// binaryFor picks the executable path for the mode.
func binaryFor(cfg *daemon.Config) string {
	switch cfg.Mode {
	case isolate.NsjailName:
		return cfg.Nsjail.Binary
	case isolate.DockerName:
		return cfg.Docker.Binary
	default:
		return ""
	}
}

// extraArgsFor picks the escape-hatch arguments for the mode.
func extraArgsFor(cfg *daemon.Config) []string {
	switch cfg.Mode {
	case isolate.NsjailName:
		return cfg.Nsjail.ExtraArgs
	case isolate.DockerName:
		return cfg.Docker.ExtraArgs
	default:
		return nil
	}
}

// checkCommands verifies, under mode=none, that every language's command resolves on the host.
//
// Why: under mode=none the command is exec'd by the host directly, so a missing PATH entry only fails on the
// first call (a very hard error to attribute). Under nsjail/docker the command resolves inside the
// rootfs/image, where a host LookPath would produce false alarms — hence the check only runs for none.
//
// ⚠️ It must validate the result of ResolveCommand (the command actually used in the current mode), not the
// generic lc.Command — otherwise a deployment with commands.none configured is falsely reported, or the check
// validates a command that never runs.
func checkCommands(cfg *daemon.Config) error {
	if cfg.Mode != isolate.NoneName {
		return nil
	}
	// Iterate the top-level language definitions rather than code.pools: language definitions now serve both
	// simplex and duplex, and under mode=none both run on the host, so every definition's entry name must
	// resolve.
	for lang, def := range cfg.Langs {
		cmd, err := def.ResolveCommand(cfg.Mode)
		if err != nil {
			return fmt.Errorf("langs.%s: %w", lang, err)
		}
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf(
				"langs.%s: command %q not found on host — mode=none needs a host-resolvable binary "+
					"(use a PATH name or an absolute path, e.g. \"d:/tools/lua5.4/lua54.exe\")",
				lang, cmd)
		}
	}
	return nil
}

// cleanOrphans reclaims orphan containers left by the isolation implementation.
//
// Only implementations of sandbox.OrphanCleaner have this concept (docker, today): after a hard kill of the
// service, reused units are not Closed in time and one-shot containers are not removeContainer'ed in time,
// and --rm does not clean up for us (it only handles the container exiting on its own).
//
// It covers both kinds: reused units and one-shot exec containers (the latter carry the sandboxd-exec label,
// which only became addressable after the "kill cleanly" rework — before that they had no label at all, and a
// leak was invisible).
func cleanOrphans(iso sandbox.Sandbox) error {
	oc, ok := iso.(sandbox.OrphanCleaner)
	if !ok {
		return nil
	}
	n, err := oc.CleanOrphans(context.Background())
	if err != nil {
		return err
	}
	if n > 0 {
		xlog.Info("sandboxd: cleaned %d orphan container(s) left by a previous run", n)
	}
	return nil
}

// registryConfig turns sandboxd's registry config into xregistry's registrar config.
// ServiceAddr must be given explicitly as an address the gateway can reach (listen may be 127.0.0.1, which is
// unreachable across machines); ServicePort defaults to the one parsed out of listen.
func registryConfig(cfg *daemon.Config) *xregistry.RegistrarConfig {
	name := cfg.Registry.ServiceName
	if name == "" {
		name = "sandboxd"
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
