package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jsvisa/bitracer/internal/api"
	"github.com/jsvisa/bitracer/internal/bot"
	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/config"
	"github.com/jsvisa/bitracer/internal/etl"
	"github.com/jsvisa/bitracer/internal/labeler"
	"github.com/jsvisa/bitracer/internal/labels"
	"github.com/jsvisa/bitracer/internal/store"
	"github.com/jsvisa/bitracer/web"
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
		runServe(ctx, cfg, os.Args[2:])
	case "migrate":
		runMigrate(ctx, cfg)
	case "run":
		etlArgs, serveArgs := splitRunArgs(os.Args[2:])
		go func() {
			if err := startETL(ctx, cfg, etlArgs); err != nil && ctx.Err() == nil {
				slog.Error("etl exited", "err", err)
				os.Exit(1)
			}
		}()
		runServe(ctx, cfg, serveArgs)
	default:
		usage()
		os.Exit(2)
	}
}

func splitRunArgs(args []string) (etlArgs, serveArgs []string) {
	etlOnly := map[string]bool{"--start-block": true, "--rpc-url": true, "--rpc-user": true, "--rpc-pass": true, "--minimum-btc": true}
	serveOnly := map[string]bool{"--listen": true, "--web-dir": true}
	take := func(name string) (bool, bool) {
		return etlOnly[name] || name == "--db-url", serveOnly[name] || name == "--db-url"
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.SplitN(a, "=", 2)[0]
		toEtl, toServe := take(name)
		if !toEtl && !toServe {
			continue
		}
		if toEtl {
			etlArgs = append(etlArgs, a)
		}
		if toServe {
			serveArgs = append(serveArgs, a)
		}
		if !strings.Contains(a, "=") && i+1 < len(args) {
			i++
			if toEtl {
				etlArgs = append(etlArgs, args[i])
			}
			if toServe {
				serveArgs = append(serveArgs, args[i])
			}
		}
	}
	return etlArgs, serveArgs
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
  --minimum-btc F            default minimum tracked movement in BTC; outputs
                             below it are not indexed and existing sub-threshold
                             rows are pruned at startup (default 0.1)
  --rpc-user U               bitcoind rpcpair user ($BTC_RPC_USER)
  --rpc-pass P               bitcoind rpcpass ($BTC_RPC_PASS)
  --db-url URL               postgres url ($DATABASE_URL)

Serve flags:
  --listen ADDR              listen address (default $BITRACER_LISTEN or :8080)
  --web-dir DIR              dashboard dir; embedded copy used when absent ($BITRACER_WEB_DIR)

Environment:
  DATABASE_URL, BTC_RPC_URL, BTC_RPC_USER, BTC_RPC_PASS,
  BLOCKSEC_LABEL_APIKEY, BLOCKSEC_LABEL_URL, BLOCKSEC_LABEL_CHAIN_ID,
  BITRACER_SYNC_INTERVAL, BITRACER_LABEL_INTERVAL, BITRACER_LISTEN, BITRACER_WEB_DIR,
  BITRACER_FANOUT_DENOM (stop on spender txs with N equal-value outputs, default 5),
  BITRACER_FANOUT_ADDRS (stop on spender txs reaching N distinct addresses, default 0 = off),
  BITRACER_FANIN_COUNT (stop when N distinct flows converge on one address,
                        default 5; suspected service sink),
  BITRACER_DECAY_PCT (stop branch outputs below this %% of the case's largest
                      seed output, default 1; 0 = off),
  BITRACER_SEED_LABELS (JSON file of known entities to preload:
                        {"addr": {"label": "...", "kind": "cex|mixer|..."}}),

Telegram bot (optional; enabled when token + LLM key are set):
  BITRACER_BOT_TELEGRAM_TOKEN  bot token from @BotFather
  BITRACER_BOT_TELEGRAM_CHATS  comma-separated chat ids allowed to ask;
                               defaults to telegram notify-channel chat_ids
  BITRACER_BOT_ADMIN_CHATS     comma-separated chat ids allowed to run write
                               actions (create/pause cases, terminal marks, ...)
  BITRACER_BOT_LLM_URL         OpenAI-compatible base url (default https://api.openai.com/v1)
  BITRACER_BOT_LLM_KEY         API key for the LLM
  BITRACER_BOT_LLM_MODEL       model name (default gpt-4o-mini)
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
	if cfg.SeedLabels != "" {
		n, err := st.LoadSeedLabelsFile(ctx, cfg.SeedLabels)
		if err != nil {
			return nil, fmt.Errorf("seed labels %s: %w", cfg.SeedLabels, err)
		}
		slog.Info("seed labels loaded", "file", cfg.SeedLabels, "new", n)
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
	slog.Info("etl starting", "rpc", cfg.RPCURL, "start_block", startBlock, "minimum_btc", float64(minSats)/1e8)
	return etl.New(st, rpc, cfg, startBlock, minSats).Run(ctx)
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

func serveFlags(cfg config.Config, args []string) (config.Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", cfg.Listen, "listen address (default $BITRACER_LISTEN or :8080)")
	dbURL := fs.String("db-url", cfg.DatabaseURL, "postgres url (default $DATABASE_URL)")
	webDir := fs.String("web-dir", cfg.WebDir, "dashboard dir; embedded copy is used when empty (default $BITRACER_WEB_DIR)")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	cfg.Listen = *listen
	cfg.DatabaseURL = *dbURL
	cfg.WebDir = *webDir
	return cfg, nil
}

func runServe(ctx context.Context, cfg config.Config, args []string) {
	cfg, err := serveFlags(cfg, args)
	if err != nil {
		slog.Error("serve failed", "err", err)
		os.Exit(1)
	}
	st, err := openStore(ctx, cfg)
	if err != nil {
		slog.Error("serve failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	rpc := btc.New(cfg.RPCURL, cfg.RPCUser, cfg.RPCPass)

	var reg *labels.Registry
	if cfg.BlocksecLabelAPIKEY != "" {
		reg = labels.NewRegistry(labels.NewBlocksec(cfg.BlocksecLabelURL, cfg.BlocksecLabelAPIKEY, cfg.BlocksecLabelChainID))
		slog.Info("label providers", "vendors", []string{"blocksec"}, "chain_id", cfg.BlocksecLabelChainID)
	} else {
		slog.Warn("no label vendor configured (BLOCKSEC_LABEL_APIKEY empty), CEX detection disabled")
	}
	lbl := labeler.New(st, reg, cfg.LabelInterval)
	go func() {
		if err := lbl.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("labeler exited", "err", err)
		}
	}()

	if bot.Enabled(cfg) {
		b := bot.New(ctx, st, cfg)
		slog.Info("telegram bot enabled", "model", cfg.BotLLMModel, "llm_url", cfg.BotLLMURL)
		go func() {
			if err := b.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("bot exited", "err", err)
			}
		}()
	} else {
		slog.Info("telegram bot disabled (set BITRACER_BOT_TELEGRAM_TOKEN + BITRACER_BOT_LLM_KEY to enable)")
	}

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: withStatic(cfg.WebDir, api.New(st, rpc, lbl).Handler()),
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
	var fsys fs.FS
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		fsys = os.DirFS(dir)
	} else if sub, err := fs.Sub(web.Dist, "dist"); err == nil {
		if index, err := fs.ReadFile(sub, "index.html"); err == nil && len(index) > 0 {
			fsys = sub
			slog.Info("serving embedded dashboard (no BITRACER_WEB_DIR on disk)")
		}
	}
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if fsys == nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" {
			if f, err := fsys.Open(strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
