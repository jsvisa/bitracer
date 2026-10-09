import { useCallback, useEffect, useState } from 'react'
import { api, type Alert, type Case, type CaseTx, type Channel, type GraphData } from './api'
import { GraphView } from './GraphView'

export function App() {
  const [cases, setCases] = useState<Case[]>([])
  const [selected, setSelected] = useState<number | null>(null)

  const refreshCases = useCallback(async () => {
    setCases(await api.listCases())
  }, [])

  useEffect(() => {
    refreshCases().catch((e) => console.error(e))
  }, [refreshCases])

  return (
    <div className="app">
      <header>
        <h1>bitracer</h1>
        <span className="sub">stolen funds tracking</span>
      </header>
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
                <span className={`badge ${c.status}`}>{c.status}</span>
              </li>
            ))}
          </ul>
        </aside>
        <main>
          {selected == null ? (
            <p className="empty">select or create a case</p>
          ) : (
            <CaseDetail key={selected} id={selected} onChanged={refreshCases} />
          )}
        </main>
      </div>
    </div>
  )
}

function NewCaseForm({ onCreated }: { onCreated: (c: Case) => void }) {
  const [name, setName] = useState('')
  const [minBTC, setMinBTC] = useState('')
  const [err, setErr] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    try {
      const c = await api.createCase(name, minBTC ? parseFloat(minBTC) : undefined)
      setName('')
      setMinBTC('')
      onCreated(c)
    } catch (e) {
      setErr(String(e))
    }
  }

  return (
    <form onSubmit={submit} className="panel">
      <h3>new case</h3>
      <input placeholder="case name" value={name} onChange={(e) => setName(e.target.value)} required />
      <input placeholder="min BTC (default 0.1)" value={minBTC} onChange={(e) => setMinBTC(e.target.value)} />
      {err && <p className="err">{err}</p>}
      <button type="submit">create case</button>
    </form>
  )
}

type Tab = 'txs' | 'channels' | 'graph' | 'alerts'

function CaseDetail({ id, onChanged }: { id: number; onChanged: () => void }) {
  const [tab, setTab] = useState<Tab>('txs')
  const [txs, setTxs] = useState<CaseTx[]>([])
  const [channels, setChannels] = useState<Channel[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])

  const refresh = useCallback(async () => {
    const [t, c] = await Promise.all([api.listCaseTxs(id), api.listChannels(id)])
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
      {tab === 'channels' && <ChannelsTab caseId={id} channels={channels} onChanged={refresh} />}
      {tab === 'graph' && <GraphTab txs={txs} />}
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
              <td className="mono">{t.txid}</td>
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
              <td colSpan={3} className="empty">no txhashes tracked yet</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

function ChannelsTab({ caseId, channels, onChanged }: { caseId: number; channels: Channel[]; onChanged: () => void }) {
  const [type, setType] = useState('slack')
  const [field1, setField1] = useState('')
  const [field2, setField2] = useState('')
  const [err, setErr] = useState('')

  const labels: Record<string, [string, string]> = {
    slack: ['webhook url', ''],
    telegram: ['bot token', 'chat id'],
    lark: ['webhook url', ''],
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    const config: Record<string, string> =
      type === 'telegram'
        ? { token: field1, chat_id: field2 }
        : { webhook: field1 }
    try {
      await api.addChannel(caseId, type, config)
      setField1('')
      setField2('')
      onChanged()
    } catch (e) {
      setErr(String(e))
    }
  }

  return (
    <div>
      <form onSubmit={submit} className="row wrap">
        <select value={type} onChange={(e) => { setType(e.target.value); setField1(''); setField2('') }}>
          <option value="slack">slack</option>
          <option value="telegram">telegram</option>
          <option value="lark">lark</option>
        </select>
        <input placeholder={labels[type][0]} value={field1} onChange={(e) => setField1(e.target.value)} required />
        {labels[type][1] && (
          <input placeholder={labels[type][1]} value={field2} onChange={(e) => setField2(e.target.value)} required />
        )}
        <button type="submit">add channel</button>
      </form>
      {err && <p className="err">{err}</p>}
      <table>
        <thead>
          <tr>
            <th>type</th>
            <th>config</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {channels.map((c) => (
            <tr key={c.id}>
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
                    await api.deleteChannel(c.id)
                    onChanged()
                  }}
                >
                  remove
                </button>
              </td>
            </tr>
          ))}
          {channels.length === 0 && (
            <tr>
              <td colSpan={3} className="empty">no channels configured</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

function GraphTab({ txs }: { txs: CaseTx[] }) {
  const [txid, setTxid] = useState('')
  const [depth, setDepth] = useState(6)
  const [data, setData] = useState<GraphData | null>(null)
  const [err, setErr] = useState('')

  const load = async (id: string, d: number) => {
    setErr('')
    try {
      setData(await api.graph(id, d))
    } catch (e) {
      setErr(String(e))
    }
  }

  return (
    <div className="graph-wrap">
      <form
        className="row wrap"
        onSubmit={(e) => {
          e.preventDefault()
          load(txid.trim(), depth)
        }}
      >
        <input
          placeholder="txhash (or pick a tracked one)"
          value={txid}
          onChange={(e) => setTxid(e.target.value)}
        />
        <select value={depth} onChange={(e) => setDepth(parseInt(e.target.value))}>
          {[3, 6, 10, 15, 20].map((d) => (
            <option key={d} value={d}>depth {d}</option>
          ))}
        </select>
        <button type="submit">draw</button>
        {txs.length > 0 && (
          <select
            value=""
            onChange={(e) => {
              if (e.target.value) {
                setTxid(e.target.value)
                load(e.target.value, depth)
              }
            }}
          >
            <option value="">tracked txhashes…</option>
            {txs.map((t) => (
              <option key={t.txid} value={t.txid}>{t.txid.slice(0, 18)}…</option>
            ))}
          </select>
        )}
      </form>
      {err && <p className="err">{err}</p>}
      {data == null ? (
        <p className="empty">enter a txhash to draw the fund flow</p>
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
          </tr>
        ))}
        {alerts.length === 0 && (
          <tr>
            <td colSpan={3} className="empty">no alerts yet</td>
          </tr>
        )}
      </tbody>
    </table>
  )
}
