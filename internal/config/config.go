package config

import (
	"os"
	"strconv"
	"time"

	"github.com/jsvisa/bitracer/internal/labels"
)

type Config struct {
	DatabaseURL          string
	RPCURL               string
	RPCUser              string
	RPCPass              string
	Listen               string
	WebDir               string
	BlocksecLabelURL     string
	BlocksecLabelAPIKEY  string
	BlocksecLabelChainID int
	LabelInterval        time.Duration
	SyncInterval         time.Duration
	FanoutAddrs          int
	FanoutDenom          int
	FaninCount           int
	DecayPct             int
	SeedLabels           string
}

func Load() Config {
	return Config{
		DatabaseURL:          env("DATABASE_URL", "postgres://localhost:5432/bitracer?sslmode=disable"),
		RPCURL:               env("BTC_RPC_URL", "http://127.0.0.1:8332"),
		RPCUser:              env("BTC_RPC_USER", ""),
		RPCPass:              env("BTC_RPC_PASS", ""),
		Listen:               env("BITRACER_LISTEN", ":8080"),
		WebDir:               env("BITRACER_WEB_DIR", "web/dist"),
		BlocksecLabelURL:     env("BLOCKSEC_LABEL_URL", labels.DefaultAPIURL),
		BlocksecLabelAPIKEY:  env("BLOCKSEC_LABEL_APIKEY", ""),
		BlocksecLabelChainID: int(envInt("BLOCKSEC_LABEL_CHAIN_ID", labels.BitcoinChainID)),
		LabelInterval:        envSec("BITRACER_LABEL_INTERVAL", 60),
		SyncInterval:         envSec("BITRACER_SYNC_INTERVAL", 15),
		FanoutDenom:          int(envInt("BITRACER_FANOUT_DENOM", 5)),
		FanoutAddrs:          int(envInt("BITRACER_FANOUT_ADDRS", 0)),
		FaninCount:           int(envInt("BITRACER_FANIN_COUNT", 5)),
		DecayPct:             int(envInt("BITRACER_DECAY_PCT", 1)),
		SeedLabels:           env("BITRACER_SEED_LABELS", ""),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envSec(key string, def int64) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(def) * time.Second
}

func envInt(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
