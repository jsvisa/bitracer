package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jsvisa/bitracer/internal/api"
	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/config"
	"github.com/jsvisa/bitracer/internal/etl"
	"github.com/jsvisa/bitracer/internal/labels"
	"github.com/jsvisa/bitracer/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "etl":
		runETL(ctx, cfg, os.Args[2:])
	case "serve":
		runServe(ctx, cfg)
	case "migrate":
		runMigrate(ctx, cfg)
	case "run":
		go func() {
			if err := startETL(ctx, cfg, os.Args[2:]); err != nil && ctx.Err() == nil {
				slog.Error("etl exited", "err", err)
				os.Exit(1)
			}
		}()
		runServe(ctx, cfg)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `bitracer — stolen funds tracking for Bitcoin

Usage:
  bitracer etl [flags]       index blocks from bitcoind and run case tracking
  bitracer serve             REST API + dashboard
  bitracer run [flags]       etl + serve in one process
  bitracer migrate           create tables

ETL flags:
  --start-block N            first block height to index (default: resume from db)
  --rpc-url URL              bitcoind RPC url (default $BTC_RPC_URL or http://127.0.0.1:8332)
  --minimum-btc F            default minimum movement size, default 0.1
  --rpc-user U               bitcoind rpcpair user ($BTC_RPC_USER)
  --rpc-pass P               bitcoind rpcpass ($BTC_RPC_PASS)
  --db-url URL               postgres url ($DATABASE_URL)

Environment:
  DATABASE_URL, BTC_RPC_URL, BTC_RPC_USER, BTC_RPC_PASS,
  BLOCKSEC_API_URL, BLOCKSEC_API_KEY, BITRACER_LISTEN, BITRACER_WEB_DIR
`)
}

func etlFlags(cfg config.Config, args []string) (config.Config, int64, int64, error) {
	fs := flag.NewFlagSet("etl", flag.ContinueOnError)
	startBlock := fs.Int64("start-block", 0, "first block height to index")
	rpcURL := fs.String("rpc-url", cfg.RPCURL, "bitcoind RPC url")
	rpcUser := fs.String("rpc-user", cfg.RPCUser, "bitcoind rpc user")
	rpcPass := fs.String("rpc-pass", cfg.RPCPass, "bitcoind rpc pass")
	dbURL := fs.String("db-url", cfg.DatabaseURL, "postgres url")
	minBTC := fs.Float64("minimum-btc", 0.1, "default minimum tracked movement in BTC")
	if err := fs.Parse(args); err != nil {
		return cfg, 0, 0, err
	}
	cfg.RPCURL = *rpcURL
	cfg.RPCUser = *rpcUser
	cfg.RPCPass = *rpcPass
	cfg.DatabaseURL = *dbURL
	return cfg, *startBlock, int64(*minBTC * 1e8), nil
}

func openStore(ctx context.Context, cfg config.Config) (*store.Store, error) {
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return st, nil
}

func startETL(ctx context.Context, cfg config.Config, args []string) error {
	cfg, startBlock, minSats, err := etlFlags(cfg, args)
	if err != nil {
		return err
	}
	st, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	rpc := btc.New(cfg.RPCURL, cfg.RPCUser, cfg.RPCPass)
	var lp labels.Provider
	if cfg.BlocksecURL != "" {
		lp = labels.NewBlocksec(cfg.BlocksecURL, cfg.BlocksecKey)
	} else {
		slog.Warn("no label provider configured (BLOCKSEC_API_URL empty), CEX detection disabled")
	}
	slog.Info("etl starting", "rpc", cfg.RPCURL, "start_block", startBlock, "minimum_btc", float64(minSats)/1e8)
	return etl.New(st, rpc, lp, cfg, startBlock, minSats).Run(ctx)
}

func runETL(ctx context.Context, cfg config.Config, args []string) {
	if err := startETL(ctx, cfg, args); err != nil && ctx.Err() == nil {
		slog.Error("etl exited", "err", err)
		os.Exit(1)
	}
}

func runMigrate(ctx context.Context, cfg config.Config) {
	st, err := openStore(ctx, cfg)
	if err != nil {
		slog.Error("migrate failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	slog.Info("migrated", "db", cfg.DatabaseURL)
}

func runServe(ctx context.Context, cfg config.Config) {
	st, err := openStore(ctx, cfg)
	if err != nil {
		slog.Error("serve failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	rpc := btc.New(cfg.RPCURL, cfg.RPCUser, cfg.RPCPass)
	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: withStatic(cfg.WebDir, api.New(st, rpc).Handler()),
	}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	slog.Info("api listening", "addr", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func withStatic(dir string, apiHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if _, err := os.Stat(filepath.Join(dir, r.URL.Path)); err == nil {
			http.ServeFile(w, r, filepath.Join(dir, r.URL.Path))
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
