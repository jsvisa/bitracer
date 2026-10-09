# AGENTS.md

Stolen-BTC fund tracker: one Go binary (`cmd/bitracer`) with subcommands `etl`
(full-chain indexer + case walker), `serve` (REST API + dashboard + labeler worker),
`migrate`, and `run` (both). Flow: bitcoind (txindex=1) → Go ETL → Postgres → API +
React/Vite dashboard.

## Verify

- Backend: `go build ./... && go vet ./...` — there are no `*_test.go` files and no CI;
  this is the only backend check.
- Frontend: `cd web && pnpm install && pnpm build` (runs `tsc -b && vite build`).
- Frontend dev: `pnpm dev` in `web/` proxies `/api` to `localhost:8080`, so run
  `bitracer serve` alongside it.

## Gotchas

- `web/embed.go` does `go:embed all:dist`, but `web/dist` is gitignored (only
  `.gitkeep` is committed). A fresh clone builds a binary that serves an empty
  dashboard — run the pnpm build first for a usable binary.
- `./build.sh` produces the Docker artifacts (the Dockerfile is network-free and just
  COPYs them). It hardcodes `GOARCH=arm64`; change it for amd64 deploys.
- Canonical env names are `BLOCKSEC_LABEL_URL` / `BLOCKSEC_LABEL_APIKEY` /
  `BLOCKSEC_LABEL_CHAIN_ID` (`-1` = bitcoin) — see `internal/config/config.go`. The
  README "Environment" section and `main.go` usage text still say
  `BLOCKSEC_API_URL` / `BLOCKSEC_API_KEY`; those are stale.
- The Go binary does not load `.env` (no dotenv lib); only `docker compose` reads it.
- Schema "migrations" are the `migrateStmts` slice of `CREATE TABLE IF NOT EXISTS`
  statements in `internal/store/store.go`, auto-applied under a pg advisory lock by
  every subcommand via `openStore` in `cmd/bitracer/main.go`. Add schema changes
  there; the standalone `migrate` subcommand is optional.

## Invariants

- The ETL never makes external HTTP calls. Label-vendor lookups (BlockSec) happen
  only in the investigate phase: the serve-side labeler loop,
  `GET /api/label`, and `POST /api/cases/{id}/resolve-labels`.
- New label vendors: implement `labels.Provider` (`Name`/`Lookup`), register in
  `labels.Registry`, wire into `runServe` in `cmd/bitracer/main.go`.
- Spends are detected on block sync only — no mempool polling (deliberate; reverted
  in commit 29b9798).
- Reorgs: a stored block-hash mismatch at height H resets all index/watch data at
  >= H. Don't break this when touching the ETL.
- Postgres is the only shared state between `etl` and `serve`; both are restart-free
  and the ETL resumes from `sync_state.last_height` (`--start-block 0` = resume).
- Full-chain indexing from an old `--start-block` is billions of rows — pick start
  blocks near case dates and expect slow local tests against real data.

## Status / workflow

Scaffold stage: works against Postgres, not yet verified against live bitcoind;
BlockSec response mapping needs confirmation against the real endpoint. Fix/feature
work goes in `.worktrees/<branch>` (gitignored, preconfigured), never direct to `main`.
