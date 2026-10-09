package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL  string
	RPCURL       string
	RPCUser      string
	RPCPass      string
	Listen       string
	WebDir       string
	BlocksecURL  string
	BlocksecKey  string
	WaitTimeout  time.Duration
	MempoolEvery time.Duration
}

func Load() Config {
	return Config{
		DatabaseURL:  env("DATABASE_URL", "postgres://localhost:5432/bitracer?sslmode=disable"),
		RPCURL:       env("BTC_RPC_URL", "http://127.0.0.1:8332"),
		RPCUser:      env("BTC_RPC_USER", ""),
		RPCPass:      env("BTC_RPC_PASS", ""),
		Listen:       env("BITRACER_LISTEN", ":8080"),
		WebDir:       env("BITRACER_WEB_DIR", "web/dist"),
		BlocksecURL:  env("BLOCKSEC_API_URL", ""),
		BlocksecKey:  env("BLOCKSEC_API_KEY", ""),
		WaitTimeout:  envSec("BITRACER_WAIT_TIMEOUT", 60),
		MempoolEvery: envSec("BITRACER_MEMPOOL_INTERVAL", 30),
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
