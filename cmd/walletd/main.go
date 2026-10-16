// walletd serves the HTTP API of package httpapi on one ledger and runs the hold expiry
// sweeper on an interval. Configuration is by environment:
//
//	LEDGER_URL         the Formance Ledger server (default http://127.0.0.1:3068)
//	LEDGER_NAME        the ledger (default wallets); created if missing
//	BIND               the listen address (default 127.0.0.1:8090)
//	HOLD_EXPIRY        default hold lifetime, a Go duration (default 168h)
//	SWEEP_INTERVAL     how often expired holds are released (default 1m; 0 disables)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/diyaa-hamza/wallet-holds/holds"
	"github.com/diyaa-hamza/wallet-holds/httpapi"
	"github.com/diyaa-hamza/wallet-holds/ledger/formance"
	"github.com/diyaa-hamza/wallet-holds/wallet"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("walletd failed", "error", err)
		os.Exit(1)
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	chart := wallet.DefaultChart()
	expiry, err := time.ParseDuration(env("HOLD_EXPIRY", "168h"))
	if err != nil {
		return err
	}
	chart.HoldExpiry = expiry
	sweep, err := time.ParseDuration(env("SWEEP_INTERVAL", "1m"))
	if err != nil {
		return err
	}

	client := formance.New(env("LEDGER_URL", "http://127.0.0.1:3068"), env("LEDGER_NAME", "wallets"))
	if err := client.EnsureLedger(ctx); err != nil {
		return err
	}
	svc, err := wallet.New(client, chart)
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: env("BIND", "127.0.0.1:8090"), Handler: httpapi.Handler(svc), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("walletd listening", "bind", srv.Addr, "ledger", client.Name(), "hold_expiry", expiry.String(), "sweep_interval", sweep.String())
		errc <- srv.ListenAndServe()
	}()
	if sweep > 0 {
		sweeper := holds.New(svc, holds.WithLogger(log))
		go func() {
			t := time.NewTicker(sweep)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if rep, err := sweeper.Run(ctx); err != nil {
						log.Error("sweep failed", "error", err)
					} else if rep.Expired > 0 || rep.Failed > 0 {
						log.Info("sweep done", "scanned", rep.Scanned, "expired", rep.Expired, "failed", rep.Failed)
					}
				}
			}
		}()
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
