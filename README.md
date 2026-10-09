# bitracer

Stolen-funds tracking for Bitcoin: given one or more source txids, follow every
movement of their outputs (>= a per-job threshold, e.g. 0.1 BTC) block by block
over bitcoind's JSON-RPC, until each path terminates at a labeled entity
(CEX, mixer, ...). Alerts go to Slack / Telegram / Lark; a local dashboard
manages tracked txids and draws the fund-flow graph.

## Components

```
bitcoind (txindex=1)
   |
   v
bitracer etl      -- JSON-RPC poller: new blocks + mempool spends, walks the
                     graph, writes everything to SQLite, resolves address
                     labels (BlockSec), fans out alerts
   |
   v
bitracer serve    -- REST API (jobs CRUD, graph, alerts) + static dashboard
```

All state lives in one SQLite file, so both processes restart-free.

## Status

Work in progress on branch `feat/scaffold`.
