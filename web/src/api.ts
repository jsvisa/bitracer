export interface Case {
  id: number
  name: string
  min_sats: number | null
  depth_cap: number
  branch_cap: number
  status: string
  created_at: string
  backfill_checkpoint: number
  backfill_target: number
}

export interface CaseTx {
  case_id: number
  txid: string
  seeded: boolean
}

export interface Channel {
  id: number
  case_id: number
  name: string
  type: string
  config: Record<string, string>
}

export interface ChannelInput {
  name: string
  type: string
  config: Record<string, string>
}

export interface Alert {
  id: number
  case_id: number
  txid: string
  address: string
  value_sats: number
  depth: number
  kind: string
  message: string
  created_at: string
}

export interface GraphNode {
  id: string
  type: string
  label: string
  value_btc: number
  cex: boolean
  cex_name?: string
  watched?: boolean
}

export interface GraphEdge {
  id: string
  source: string
  target: string
  value_btc: number
  txid: string
  height: number
  time?: number
}

export interface GraphData {
  nodes: GraphNode[]
  edges: GraphEdge[]
  txids: string[]
}

export interface SyncStatus {
  last_height: number
  last_block_ts: number
  updated_at: string
  chain_height: number | null
  lag_blocks: number | null
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export const api = {
  listCases: () => req<Case[]>('/api/cases'),
  createCase: (name: string, minBTC?: number, channel?: ChannelInput) =>
    req<Case>('/api/cases', {
      method: 'POST',
      body: JSON.stringify({ name, min_btc: minBTC, channel }),
    }),
  deleteCase: (id: number) => req<void>(`/api/cases/${id}`, { method: 'DELETE' }),
  setCaseStatus: (id: number, status: string) =>
    req<void>(`/api/cases/${id}`, { method: 'PATCH', body: JSON.stringify({ status }) }),

  listCaseTxs: (id: number) => req<CaseTx[]>(`/api/cases/${id}/txs`),
  addCaseTx: (id: number, txid: string) =>
    req<void>(`/api/cases/${id}/txs`, { method: 'POST', body: JSON.stringify({ txid }) }),
  deleteCaseTx: (id: number, txid: string) =>
    req<void>(`/api/cases/${id}/txs/${txid}`, { method: 'DELETE' }),

  listChannels: (id: number) => req<Channel[]>(`/api/cases/${id}/channels`),
  addChannel: (id: number, name: string, type: string, config: Record<string, string>) =>
    req<Channel>(`/api/cases/${id}/channels`, { method: 'POST', body: JSON.stringify({ name, type, config }) }),
  testChannel: (type: string, config: Record<string, string>) =>
    req<void>('/api/channels/test', { method: 'POST', body: JSON.stringify({ type, config }) }),
  deleteChannel: (channelID: number) => req<void>(`/api/channels/${channelID}`, { method: 'DELETE' }),

  listAlerts: (caseID?: number) =>
    req<Alert[]>(`/api/alerts?limit=200${caseID ? `&case_id=${caseID}` : ''}`),

  syncStatus: () => req<SyncStatus>('/api/sync'),

  caseGraph: (id: number, depth = 6, minSats?: number) =>
    req<GraphData>(
      `/api/graph?case_id=${id}&depth=${depth}${minSats != null ? `&min_sats=${minSats}` : ''}`,
    ),
}

export const satsToBTC = (s: number) => s / 1e8

export const mempoolTx = (txid: string) => `https://mempool.space/tx/${txid}`
