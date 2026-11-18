// Command sandboxg 是 sandboxd 的网关：对 nexus 暴露与 sandboxd 相同的 /v1/*
// 协议，对后端做负载均衡（exec/code）与粘性路由（interactive，按 session_id）。
//
// 协议透明：不重写帧，只做「读 open 帧 → 选后端 → 双向透传」。多实例部署时
// 粘性绑定存 redis（共享），保证同一 session 尽量打到同一 sandboxd。
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
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := gw.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// 初始化日志（xlog 需先 Init，否则 Info/Error 会 panic）
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
