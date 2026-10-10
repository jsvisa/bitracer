# bitracer

Stolen-funds tracking for Bitcoin. Given one or more source txhashes ("cases"),
follow every movement of their outputs (above a minimum threshold, e.g. 0.1 BTC)
block by block over bitcoind's JSON-RPC until each path terminates at a terminal
entity — an exchange (CEX), mixer, gambling/darknet/service address (via the
BlockSec address-label API or manual pinning), or a mixer-shaped spender tx
(coinjoin denominations). Alerts fan out to Slack / Telegram / Lark channels —
configured once globally and picked per case — via the web dashboard, which
also draws the fund-flow graph from the local Postgres index.

## Architecture

```
bitcoind (txindex=1)
   |  JSON-RPC (getblock 2, getblockchaininfo, getrawtransaction, getblockheader)
   v
bitracer etl      full-chain indexer + case walker (no external calls)
  - indexes every tx/output/input from --start-block into Postgres
  - follows the tip by polling getblockchaininfo (BITRACER_SYNC_INTERVAL)
  - watches case source txhashes and every descendant output >= min threshold
  - reorg-safe (block-hash check + reset window)
  - stop-at-terminal-entity (CEX/mixer/service via the local label cache) and
    stop-at-mixer-shaped spender tx (equal-value denominations)
   |
   v
Postgres          txs, tx_outputs, tx_inputs (chain index)
                  cases, case_txs, channels, case_channel_subs,
                  watched_outputs, addresses (label cache), alerts, sync_state
   |
   v
bitracer serve    REST API + dashboard + labeler worker (investigate phase)
  - cases CRUD, txhashes per case, global notify channels + per-case subscriptions
  - labeler: resolves addresses via pluggable vendors (BlockSec, ...),
    caches results, flips watched outputs to terminal on terminal-entity hits
    (exchange, mixer, gambling, darknet, service)
  - /api/graph: BFS over the spend index (tx<->address bipartite graph)
```

Label vendors are pluggable (`labels.Provider` interface, fan-out `Registry`).
BlockSec is wired (`BLOCKSEC_LABEL_APIKEY`, `BLOCKSEC_LABEL_CHAIN_ID=-1` for
bitcoin); add a vendor by implementing `Name()/Lookup()` and registering it.
Vendor lookups happen only in the investigate phase (serve-side labeler loop +
`GET /api/label?address=` + `POST /api/cases/{id}/resolve-labels`), never in
the ETL.

A **case** = one investigation: one or more source txhashes and a minimum-BTC
threshold (falls back to the daemon's `--minimum-btc`). Notification channels
(slack webhook / telegram bot token+chat / lark webhook) are global config —
managed once from the dashboard's channels panel — and each case picks which of
them receive its alerts. All state is in Postgres, so both processes
restart-free and the ETL resumes from `sync_state.last_height`.

## Requirements

- bitcoind with `txindex=1`, RPC enabled
- Postgres (14+)
- Go 1.26+, Node 20+ / pnpm (only for building the dashboard)

## Build

```sh
(cd web && pnpm install && pnpm build)   # produces web/dist, embedded via web/embed.go
go build -o bitracer ./cmd/bitracer
```

The dashboard is embedded into the binary at build time (`go:embed all:dist`), so
run the pnpm build **before** `go build`; without it the binary serves an empty
dashboard. `serve` prefers the on-disk `BITRACER_WEB_DIR`/`--web-dir` when it has
an `index.html`, and falls back to the embedded copy.

## Docker

`./build.sh` produces the image artifacts (cross-compiles `GOARCH=arm64` — change
for amd64); the Dockerfile is network-free and just COPYs them. `docker compose up`
runs the full stack (postgres + `bitracer run`); compose reads `.env` (see
`.env.example`), which the Go binary itself does not load.

## Run

```sh
# schema
DATABASE_URL=postgres://... ./bitracer migrate

# daemon: index from block N onwards, track movements >= 0.1 BTC (default)
./bitracer etl \
  --start-block 870000 \
  --rpc-url http://127.0.0.1:8332 \
  --rpc-user user --rpc-pass pass \
  --minimum-btc 0.1 \
  --db-url postgres://...

# api + dashboard (REST API under /api, static dashboard)
DATABASE_URL=postgres://... BLOCKSEC_LABEL_APIKEY=... ./bitracer serve

# or both in one process
./bitracer run --start-block 870000 ...
```

Environment: `DATABASE_URL`, `BTC_RPC_URL`, `BTC_RPC_USER`, `BTC_RPC_PASS`,
`BLOCKSEC_LABEL_APIKEY`, `BLOCKSEC_LABEL_URL`, `BLOCKSEC_LABEL_CHAIN_ID`
(`-1` = bitcoin), `BITRACER_SYNC_INTERVAL` (default 15s), `BITRACER_LABEL_INTERVAL`
(60s), `BITRACER_LISTEN` (default `:8080`), `BITRACER_WEB_DIR` (default `web/dist`),
`BITRACER_FANOUT_DENOM` (stop when a spender tx has ≥ N outputs of one exact
value — the coinjoin/mixer signature; default 5), `BITRACER_FANOUT_ADDRS`
(stop when a spender tx pays ≥ N distinct addresses; default 0 = off — too
eager for exchange payout sweeps and thief splits), `BITRACER_FANIN_COUNT`
(stop when ≥ N distinct flows converge on one unlabeled address — the
service/exchange sink signature; default 5, kind `fanin`),
`BITRACER_DECAY_PCT` (don't follow branch outputs below this percentage of the
case's largest seed output; default 1, 0 = off), `BITRACER_SEED_LABELS` (path
to a JSON file of known entities, inserted without clobbering vendor labels or
manual pins):

```json
{
  "bc1q…mixer-address": { "label": "Known Mixer", "kind": "mixer" },
  "1A…exchange-hot":    { "label": "Exchange Hot 1", "kind": "cex" },
  "3C…attributed-only": { "label": "Some Entity", "kind": "" }
}
```

Entries with a `kind` are terminal stops; `"kind": ""` is attribution only.

### Telegram bot (two-way chat)

`serve` (and `run`) can run an LLM assistant inside your Telegram alert chats:
ask "where are case 1's funds parked?", "show case 2's movement history",
"what is address bc1q…?", "indexer status" — the bot answers from the live
store via tool calls to an OpenAI-compatible LLM. Admin chats can also make
changes: create/pause cases, add seed txs, mark txs/addresses terminal, send
test alerts.

```
BITRACER_BOT_TELEGRAM_TOKEN  bot token from @BotFather
BITRACER_BOT_TELEGRAM_CHATS  chat ids allowed to ask (defaults to the chat_ids
                             of configured telegram notify channels)
BITRACER_BOT_ADMIN_CHATS     chat ids allowed to run write actions
BITRACER_BOT_LLM_URL         OpenAI-compatible base url (default https://api.openai.com/v1)
BITRACER_BOT_LLM_KEY         LLM API key (any non-empty value for local Ollama)
BITRACER_BOT_LLM_MODEL       model (default gpt-4o-mini)
```

The bot is enabled when token + LLM key are set. It answers read questions
only in allowed chats, and write actions only in admin chats — non-allowed
chats are ignored. In private chats it answers every message; in groups only
`@mentions` of the bot (the handle is stripped before answering). Updates
queued before startup are skipped, so redeploys don't replay stale questions.
Note the bot must be the only consumer of its token's
`getUpdates` (remove any webhook first).

### Dashboard

- create a **case** (name, optional min BTC) and pick which notify channels it uses
- add/remove **txhashes** — the ETL seeds each source tx and starts walking
- **channels** panel (header): manage the global notify channels
  (slack / telegram / lark) — cases subscribe to these
- **graph** tab: enter any txhash to draw the fund flow (also works for txs not
  tracked, if bitcoind has them); red nodes = labeled entities (CEX)
- **alerts** tab: live feed (polls every 15s)

### API

```
GET    /api/health
GET    /api/cases                  POST /api/cases {name, min_btc?, depth_cap?, branch_cap?, channel_ids?}
GET    /api/cases/{id}             PATCH /api/cases/{id} {status: active|paused}
DELETE /api/cases/{id}             (cascades txs/subs/watched/alerts)
GET    /api/cases/{id}/txs         POST /api/cases/{id}/txs {txid}
DELETE /api/cases/{id}/txs/{txid}
GET    /api/channels               POST /api/channels {name, type, config}
DELETE /api/channels/{id}          POST /api/channels/test {type, config}
GET    /api/cases/{id}/channels    PUT /api/cases/{id}/channels {channel_ids: []}
GET    /api/alerts?case_id=&limit=
GET    /api/graph?txid=&depth=
GET    /api/label?address=
POST   /api/cases/{id}/resolve-labels
POST   /api/addresses/{address}/terminal   {kind: cex|mixer|service|gambling|darknet|manual}
DELETE /api/addresses/{address}/terminal
```

Channel configs: slack `{"webhook": "https://hooks.slack.com/...", "channel":
"#alerts"}` (`channel` is optional; overrides the webhook's bound channel on
legacy incoming webhooks),
telegram `{"token": "...", "chat_id": "..."}`
lark `{"webhook": "https://open.larksuite.com/open-apis/bot/v2/hook/..."}`.

## Semantics & caveats

- An output is watched only if `value >= case.min_sats` (or the daemon default);
  dust branches are not followed.
- A path stops (output flips to `terminal`) when the address is a terminal
  entity: CEX/exchange, mixer, gambling, darknet or service per the BlockSec
  category mapping, or any address pinned by hand via
  `POST /api/addresses/{address}/terminal` — covers services the vendor does
  not label. It also stops when the spender tx looks like a mixer/coinjoin
  (see `BITRACER_FANOUT_DENOM`/`BITRACER_FANOUT_ADDRS`): mixer outflow is
  mostly unrelated churn, so following it would only explode the branch tree.
  Every stop emits an alert whose kind names the reason
  (`cex`/`mixer`/`service`/`fanout`/...), or at `depth_cap` / `branch_cap`.
- **Fan-in stop**: when ≥ `BITRACER_FANIN_COUNT` distinct flows converge on
  one unlabeled address, it is marked terminal (kind `fanin`, red — a
  suspicion worth reviewing, not a confirmed entity): services and exchange
  hot wallets are where unrelated flows meet.
- **Value-decay floor**: branch outputs below `BITRACER_DECAY_PCT` % of the
  case's largest seed output are not followed, keeping long small-value
  tails bounded.
- Reorgs: when the stored block hash at height H mismatches the chain, all
  index/watch data at >= H is reset and re-synced.
- Spends are detected on block sync only (no mempool polling); alerts arrive
  once the spending block is indexed.
- Retracking: `DELETE /api/cases/{id}/txs/{txid}` drops the case's whole
  watched-outputs tree (it has no seed lineage) and resets the remaining case
  txs to unseeded; re-adding a txhash then makes the ETL re-seed, re-walk, and
  re-fire alerts (seed/spend/cex) to the case's subscribed channels.
- Full-chain indexing from an old `--start-block` is heavy (billions of rows for
  whole-chain scans) — pick a start block near your case dates for reasonable
  footprint, and give Postgres real resources.
- `blockheight`-aware seeding: source txhashes older than `--start-block` are
  fetched directly from bitcoind.

## Status

Scaffold complete: backend builds + vets clean (no tests yet — `go build ./... &&
go vet ./...` is the only backend check), CRUD API smoke-tested against Postgres 16,
dashboard builds. Not yet verified against a live bitcoind; BlockSec response
mapping is tolerant but should be confirmed against the real endpoint shape.
