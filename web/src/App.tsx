import { useCallback, useEffect, useState } from 'react'
import { api, mempoolTx, type Alert, type Case, type CaseTx, type Channel, type GraphData, type SyncStatus } from './api'
import { GraphView } from './GraphView'

export function App() {
  const [cases, setCases] = useState<Case[]>([])
  const [selected, setSelected] = useState<number | null>(null)
  const [showChannels, setShowChannels] = useState(false)

  const refreshCases = useCallback(async () => {
    setCases(await api.listCases())
  }, [])

  useEffect(() => {
    refreshCases().catch((e) => console.error(e))
  }, [refreshCases])

  useEffect(() => {
    const t = setInterval(() => refreshCases().catch(() => {}), 15000)
    return () => clearInterval(t)
  }, [refreshCases])

  return (
    <div className="app">
      <header>
        <h1>bitracer</h1>
        <span className="sub">stolen funds tracking</span>
        <SyncBadge />
        <button onClick={() => setShowChannels(true)}>channels</button>
      </header>
      {showChannels && <ChannelsManager onClose={() => setShowChannels(false)} />}
      <div className="layout">
        <aside>
          <NewCaseForm onCreated={(c) => { setCases([c, ...cases]); setSelected(c.id) }} />
          <ul className="case-list">
            {cases.map((c) => (
              <li
                key={c.id}
                className={c.id === selected ? 'selected' : ''}
                onClick={() => setSelected(c.id)}
              >
                <span className="case-name">#{c.id} {c.name}</span>
                {c.tx_total > 0 &&
                  (c.tx_seeded < c.tx_total ? (
                    <span
                      className="badge pending"
                      title={`${c.tx_total - c.tx_seeded} of ${c.tx_total} txhashes awaiting the seed walk (retrack pending or etl down)`}
                    >
                      seeding {c.tx_seeded}/{c.tx_total}
                    </span>
                  ) : (
                    <span
                      className="badge tracking"
                      title={`all ${c.tx_total} txhashes seeded — spends alert on block sync`}
                    >
                      tracking {c.tx_total}
                    </span>
                  ))}
                {c.backfill_target > 0 && c.backfill_checkpoint > 0 && c.backfill_checkpoint < c.backfill_target && (
                  <span
                    className="badge backfill"
                    title={`backfill checkpoint ${c.backfill_checkpoint.toLocaleString()} → target ${c.backfill_target.toLocaleString()}`}
                  >
                    backfill {Math.min(99, Math.floor((c.backfill_checkpoint / c.backfill_target) * 100))}%
                  </span>
                )}
                <span className={`badge ${c.status}`}>{c.status}</span>
              </li>
            ))}
          </ul>
        </aside>
        <main>
          {selected == null ? (
            <p className="empty">select or create a case</p>
          ) : (
            <CaseDetail
              key={selected}
              id={selected}
              minSats={cases.find((c) => c.id === selected)?.min_sats ?? null}
              onChanged={refreshCases}
            />
          )}
        </main>
      </div>
    </div>
  )
}

const STALE_MS = 30 * 60 * 1000

function SyncBadge() {
  const [status, setStatus] = useState<SyncStatus | null>(null)

  useEffect(() => {
    const load = () => api.syncStatus().then(setStatus).catch(() => {})
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  }, [])

  if (!status) return null

  const updatedMs = Date.parse(status.updated_at)
  const stale = Number.isNaN(updatedMs) || Date.now() - updatedMs > STALE_MS
  const behind = status.lag_blocks != null && status.lag_blocks > 0
  const cls = stale ? 'stale' : behind ? 'syncing' : 'ok'
  const title = stale
    ? `walker has not advanced since ${status.updated_at}`
    : `walker last advanced ${status.updated_at}`

  return (
    <span className={`sync ${cls}`} title={title}>
      <span className="dot" />
      <span>
        synced {status.last_height.toLocaleString()}
        {status.chain_height != null && ` / ${status.chain_height.toLocaleString()}`}
        {behind && ` · behind ${status.lag_blocks!.toLocaleString()} blocks`}
        {status.last_block_ts > 0 && ` · tip ${ago(status.last_block_ts * 1000)} ago`}
      </span>
    </span>
  )
}

function ago(ts: number): string {
  const s = Math.max(0, Math.floor((Date.now() - ts) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

const channelLabels: Record<string, [string, string]> = {
  slack: ['webhook url', 'channel name (optional)'],
  telegram: ['bot token', 'chat id'],
  lark: ['webhook url', ''],
}

function field2Required(type: string): boolean {
  return type === 'telegram'
}

function channelConfig(type: string, field1: string, field2: string): Record<string, string> {
  if (type === 'telegram') return { token: field1.trim(), chat_id: field2.trim() }
  if (type === 'slack') {
    const cfg: Record<string, string> = { webhook: field1.trim() }
    if (field2.trim()) cfg.channel = field2.trim()
    return cfg
  }
  return { webhook: field1.trim() }
}

function NewCaseForm({ onCreated }: { onCreated: (c: Case) => void }) {
  const [name, setName] = useState('')
  const [minBTC, setMinBTC] = useState('')
  const [channels, setChannels] = useState<Channel[]>([])
  const [picked, setPicked] = useState<number[]>([])
  const [err, setErr] = useState('')

  useEffect(() => {
    api.listChannels().then(setChannels).catch(() => {})
  }, [])

  const toggle = (id: number) =>
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    if (picked.length === 0) {
      setErr('bind at least one notify channel before creating the case')
      return
    }
    try {
      const c = await api.createCase(name, minBTC ? parseFloat(minBTC) : undefined, picked)
      setName('')
      setMinBTC('')
      setPicked([])
      onCreated(c)
    } catch (e) {
      setErr(String(e))
    }
  }

  return (
    <form onSubmit={submit} className="panel">
      <h3>new case</h3>
      <p className="sub">step 1 — bind channels, then add source txhashes after the case opens</p>
      <input placeholder="case name" value={name} onChange={(e) => setName(e.target.value)} required />
      <input placeholder="min BTC (default 0.1)" value={minBTC} onChange={(e) => setMinBTC(e.target.value)} />
      <div className="pick-list">
        {channels.length === 0 ? (
          <p className="empty">no notify channels yet — add one from the channels panel first</p>
        ) : (
          channels.map((ch) => (
            <label key={ch.id} className="pick-row">
              <input type="checkbox" checked={picked.includes(ch.id)} onChange={() => toggle(ch.id)} />
              <span className="pick-name">{ch.name}</span>
              <span className="badge">{ch.type}</span>
            </label>
          ))
        )}
      </div>
      <p className="sub">pick at least one channel — case alerts fan out to it</p>
      {err && <p className="err">{err}</p>}
      <button type="submit" disabled={picked.length === 0} title={picked.length === 0 ? 'bind at least one channel first' : ''}>
        create case
      </button>
    </form>
  )
}

type Tab = 'txs' | 'channels' | 'graph' | 'alerts'

function CaseDetail({ id, minSats, onChanged }: { id: number; minSats: number | null; onChanged: () => void }) {
  const [tab, setTab] = useState<Tab>('txs')
  const [txs, setTxs] = useState<CaseTx[]>([])
  const [channels, setChannels] = useState<Channel[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])

  const refresh = useCallback(async () => {
    const [t, c] = await Promise.all([api.listCaseTxs(id), api.listCaseChannels(id)])
    setTxs(t)
    setChannels(c)
  }, [id])

  useEffect(() => {
    refresh().catch((e) => console.error(e))
  }, [refresh])

  useEffect(() => {
    const load = () => api.listAlerts(id).then(setAlerts).catch(() => {})
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  }, [id])

  return (
    <div className="detail">
      <div className="tabs">
        {(['txs', 'channels', 'graph', 'alerts'] as Tab[]).map((t) => (
          <button key={t} className={tab === t ? 'active' : ''} onClick={() => setTab(t)}>
            {t}
            {t === 'alerts' && alerts.length > 0 ? ` (${alerts.length})` : ''}
          </button>
        ))}
        <span className="spacer" />
        <button
          className="danger"
          onClick={async () => {
            if (!confirm(`delete case #${id}?`)) return
            await api.deleteCase(id)
            onChanged()
          }}
        >
          delete case
        </button>
      </div>
      {tab === 'txs' && <TxsTab caseId={id} txs={txs} onChanged={refresh} />}
      {tab === 'channels' && <ChannelsTab caseId={id} subscribed={channels} onChanged={refresh} />}
      {tab === 'graph' && <GraphTab caseId={id} txs={txs} minSats={minSats} />}
      {tab === 'alerts' && <AlertsTab alerts={alerts} />}
    </div>
  )
}

function TxsTab({ caseId, txs, onChanged }: { caseId: number; txs: CaseTx[]; onChanged: () => void }) {
  const [txid, setTxid] = useState('')
  const [err, setErr] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    try {
      await api.addCaseTx(caseId, txid.trim())
      setTxid('')
      onChanged()
    } catch (e) {
      setErr(String(e))
    }
  }

  return (
    <div>
      <form onSubmit={submit} className="row">
        <input
          placeholder="source txhash (64 hex chars)"
          value={txid}
          onChange={(e) => setTxid(e.target.value)}
          required
        />
        <button type="submit">track txhash</button>
      </form>
      {err && <p className="err">{err}</p>}
      <table>
        <thead>
          <tr>
            <th>txhash</th>
            <th>seeded</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {txs.map((t) => (
            <tr key={t.txid}>
              <td className="mono">
                <a href={mempoolTx(t.txid)} target="_blank" rel="noreferrer">
                  {t.txid}
                </a>
              </td>
              <td>{t.seeded ? 'yes' : 'pending'}</td>
              <td>
                <button
                  className="danger"
                  onClick={async () => {
                    await api.deleteCaseTx(caseId, t.txid)
                    onChanged()
                  }}
                >
                  remove
                </button>
              </td>
            </tr>
          ))}
          {txs.length === 0 && (
            <tr>
              <td colSpan={3} className="empty">no txhashes tracked yet — step 2: add the source txhash(es) above</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

function ChannelsTab({ caseId, subscribed, onChanged }: { caseId: number; subscribed: Channel[]; onChanged: () => void }) {
  const [all, setAll] = useState<Channel[]>([])
  const [err, setErr] = useState('')

  useEffect(() => {
    api.listChannels().then(setAll).catch((e) => setErr(String(e)))
  }, [])

  const toggle = async (ch: Channel) => {
    setErr('')
    if (subscribed.length === 1 && subscribed.some((c) => c.id === ch.id)) {
      setErr('at least one channel must stay bound to the case')
      return
    }
    const ids = subscribed.some((c) => c.id === ch.id)
      ? subscribed.filter((c) => c.id !== ch.id).map((c) => c.id)
      : [...subscribed.map((c) => c.id), ch.id]
    try {
      await api.setCaseChannels(caseId, ids)
      onChanged()
    } catch (e) {
      setErr(String(e))
    }
  }

  return (
    <div>
      <p className="sub">pick which global notify channels receive this case's alerts — manage channels from the header panel. at least one stays bound.</p>
      {err && <p className="err">{err}</p>}
      <table>
        <thead>
          <tr>
            <th>subscribed</th>
            <th>name</th>
            <th>type</th>
            <th>config</th>
          </tr>
        </thead>
        <tbody>
          {all.map((c) => (
            <tr key={c.id} className={subscribed.some((s) => s.id === c.id) ? 'subbed' : ''}>
              <td>
                <input
                  type="checkbox"
                  checked={subscribed.some((s) => s.id === c.id)}
                  disabled={subscribed.length === 1 && subscribed.some((s) => s.id === c.id)}
                  title={subscribed.length === 1 && subscribed.some((s) => s.id === c.id) ? 'at least one channel must stay bound' : ''}
                  onChange={() => toggle(c)}
                />
              </td>
              <td>{c.name}</td>
              <td>{c.type}</td>
              <td className="mono">
                {Object.entries(c.config)
                  .map(([k, v]) => `${k}=${v.length > 24 ? v.slice(0, 24) + '…' : v}`)
                  .join(' ')}
              </td>
            </tr>
          ))}
          {all.length === 0 && (
            <tr>
              <td colSpan={4} className="empty">no channels configured — add one from the channels panel in the header</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

function ChannelsManager({ onClose }: { onClose: () => void }) {
  const [channels, setChannels] = useState<Channel[]>([])
  const [err, setErr] = useState('')

  const load = useCallback(() => api.listChannels().then(setChannels), [])

  useEffect(() => {
    load().catch((e) => setErr(String(e)))
  }, [load])

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="gd-head">
          <span>notify channels — global config</span>
          <button onClick={onClose}>close</button>
        </div>
        <ChannelForm onCreated={load} />
        {err && <p className="err">{err}</p>}
        <table>
          <thead>
            <tr>
              <th>name</th>
              <th>type</th>
              <th>config</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {channels.map((c) => (
              <tr key={c.id}>
                <td>{c.name}</td>
                <td>{c.type}</td>
                <td className="mono">
                  {Object.entries(c.config)
                    .map(([k, v]) => `${k}=${v.length > 24 ? v.slice(0, 24) + '…' : v}`)
                    .join(' ')}
                </td>
                <td>
                  <button
                    className="danger"
                    onClick={async () => {
                      if (!confirm(`delete channel "${c.name}"? cases subscribed to it will stop notifying`)) return
                      setErr('')
                      try {
                        await api.deleteChannel(c.id)
                        await load()
                      } catch (e) {
                        setErr(String(e))
                      }
                    }}
                  >
                    remove
                  </button>
                </td>
              </tr>
            ))}
            {channels.length === 0 && (
              <tr>
                <td colSpan={4} className="empty">no channels configured</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function ChannelForm({ onCreated }: { onCreated: () => void }) {
  const [type, setType] = useState('slack')
  const [name, setName] = useState('')
  const [field1, setField1] = useState('')
  const [field2, setField2] = useState('')
  const [err, setErr] = useState('')
  const [testState, setTestState] = useState<'idle' | 'sending' | 'ok' | 'fail'>('idle')

  const resetFields = () => {
    setName('')
    setField1('')
    setField2('')
    setTestState('idle')
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    try {
      await api.createChannel(name.trim(), type, channelConfig(type, field1, field2))
      resetFields()
      onCreated()
    } catch (e) {
      setErr(String(e))
    }
  }

  const test = async () => {
    setErr('')
    if (!field1.trim() || (field2Required(type) && !field2.trim())) {
      setTestState('fail')
      setErr('fill in the channel fields before testing')
      return
    }
    setTestState('sending')
    try {
      await api.testChannel(type, channelConfig(type, field1, field2))
      setTestState('ok')
    } catch (e) {
      setTestState('fail')
      setErr(String(e))
    }
  }

  return (
    <form onSubmit={submit} className="row wrap">
      <select value={type} onChange={(e) => { setType(e.target.value); resetFields() }}>
        <option value="slack">slack</option>
        <option value="telegram">telegram</option>
        <option value="lark">lark</option>
      </select>
      <input placeholder="channel name (unique)" value={name} onChange={(e) => setName(e.target.value)} required />
      <input placeholder={channelLabels[type][0]} value={field1} onChange={(e) => setField1(e.target.value)} required />
      {channelLabels[type][1] && (
        <input
          placeholder={channelLabels[type][1]}
          value={field2}
          onChange={(e) => setField2(e.target.value)}
          required={field2Required(type)}
        />
      )}
      <button type="button" onClick={test} disabled={testState === 'sending'}>
        {testState === 'sending' ? 'testing…' : 'test'}
      </button>
      <button type="submit">add channel</button>
      {testState === 'ok' && <p className="ok">test message sent — check the channel</p>}
      {err && <p className="err">{err}</p>}
    </form>
  )
}

function GraphTab({ caseId, txs, minSats }: { caseId: number; txs: CaseTx[]; minSats: number | null }) {
  const [depth, setDepth] = useState(6)
  const [data, setData] = useState<GraphData | null>(null)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const threshold = minSats ?? 10_000_000

  const load = useCallback(
    async (d: number) => {
      setLoading(true)
      setErr('')
      try {
        setData(await api.caseGraph(caseId, d, threshold))
      } catch (e) {
        setErr(String(e))
        setData(null)
      } finally {
        setLoading(false)
      }
    },
    [caseId, threshold],
  )

  useEffect(() => {
    load(depth)
  }, [load])

  return (
    <div className="graph-wrap">
      <form
        className="row wrap"
        onSubmit={(e) => {
          e.preventDefault()
          load(depth)
        }}
      >
        <span>
          case #{caseId}: {txs.length} tracked txhash{txs.length === 1 ? '' : 'es'}
        </span>
        <select value={depth} onChange={(e) => setDepth(parseInt(e.target.value))}>
          {[3, 6, 10, 15, 20].map((d) => (
            <option key={d} value={d}>depth {d}</option>
          ))}
        </select>
        <button type="submit" disabled={loading}>
          {loading ? 'drawing…' : 'redraw'}
        </button>
      </form>
      {err && <p className="err">{err}</p>}
      {data == null ? (
        <p className="empty">{loading ? 'drawing case fund flow…' : 'no graph data'}</p>
      ) : data.nodes.length === 0 ? (
        <p className="empty">no outputs at or above the {(threshold / 1e8).toFixed(2)} BTC threshold</p>
      ) : (
        <GraphView data={data} />
      )}
    </div>
  )
}

function AlertsTab({ alerts }: { alerts: Alert[] }) {
  return (
    <table>
      <thead>
        <tr>
          <th>time</th>
          <th>kind</th>
          <th>message</th>
          <th>tx</th>
        </tr>
      </thead>
      <tbody>
        {alerts.map((a) => (
          <tr key={a.id}>
            <td className="mono">{a.created_at}</td>
            <td>
              <span className={`badge ${a.kind === 'cex' ? 'cex' : a.kind}`}>{a.kind}</span>
            </td>
            <td>{a.message}</td>
            <td className="mono">
              <a href={mempoolTx(a.txid)} target="_blank" rel="noreferrer">
                {a.txid.length > 14 ? a.txid.slice(0, 12) + '…' : a.txid}
              </a>
            </td>
          </tr>
        ))}
        {alerts.length === 0 && (
          <tr>
            <td colSpan={4} className="empty">no alerts yet</td>
          </tr>
        )}
      </tbody>
    </table>
  )
}
