// setpointd is the control plane daemon: it wires the Store, the
// Providers, the Reconciler, and the REST server together. This module is
// the only place core and providers meet (ADR-0011) — core never imports a
// provider.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tonyrosario/setpoint/core/provider"
	"github.com/tonyrosario/setpoint/core/reconcile"
	"github.com/tonyrosario/setpoint/core/server"
	"github.com/tonyrosario/setpoint/core/store"
	dockerprovider "github.com/tonyrosario/setpoint/providers/docker"
)

func main() {
	listen := flag.String("listen", envOr("SETPOINT_LISTEN", ":8080"), "address for the REST API")
	interval := flag.Duration("sweep-interval", 3*time.Second, "reconcile sweep interval")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	docker, err := dockerprovider.New()
	if err != nil {
		log.Error("docker provider", "error", err)
		os.Exit(1)
	}
	dockerNetwork, err := dockerprovider.NewNetwork()
	if err != nil {
		log.Error("docker network provider", "error", err)
		os.Exit(1)
	}

	st := store.NewMemory()
	rec := reconcile.New(st, []provider.Provider{docker, dockerNetwork}, *interval, log)
	srv := server.New(st, rec.Nudge)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go rec.Run(ctx)

	httpServer := &http.Server{Addr: *listen, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error("graceful shutdown", "error", err)
		}
	}()

	log.Info("setpointd listening", "addr", *listen, "sweepInterval", *interval)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
