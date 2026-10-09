import { useCallback, useEffect, useRef, useState } from 'react'
import dagre from '@dagrejs/dagre'
import type { GraphData } from './api'

const ORANGE = '#f38a2f'
const CARD = '#24262b'
const CARD_BORDER = '#3d4148'
const TEXT = '#e8eaed'
const MUTED = '#9aa0a6'

interface FlowNode {
  id: string
  kind: 'address' | 'tx'
  label: string
  full: string
  value: number
  cex: boolean
  cexName: string
  terminal: string
  highlighted: boolean
}

interface FlowEdge {
  id: string
  source: string
  target: string
  value: number
  height: number
  time: number
  txid: string
}

interface LaidNode extends FlowNode {
  x: number
  y: number
  w: number
  h: number
}

interface LaidEdge extends FlowEdge {
  path: string
  lx: number
  ly: number
  fontSize: number
}

interface Layout {
  nodes: LaidNode[]
  edges: LaidEdge[]
  bbox: { minX: number; minY: number; w: number; h: number }
}

type Sel = { kind: 'node' | 'edge'; id: string } | null

const fmtBTC = (v: number) =>
  `${v.toFixed(8).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')} BTC`

const fmtTime = (t: number | undefined) =>
  t && t > 0 ? new Date(t * 1000).toLocaleString('sv-SE', { hour12: false }) : ''

const fmtTimeShort = (t: number | undefined) => {
  const s = fmtTime(t)
  return s ? s.slice(0, 16) : ''
}

const stripKind = (id: string) => id.replace(/^[at]:/, '')

const edgeLabel = (e: FlowEdge) => {
  const parts: string[] = []
  if (e.height > 0) parts.push(`[${e.height}]`)
  const t = fmtTimeShort(e.time)
  if (t) parts.push(`[${t}]`)
  parts.push(fmtBTC(e.value))
  return parts.join(' ')
}

const shortTxid = (id: string) => (id.length <= 12 ? id : `${id.slice(0, 10)}…`)

function collapse(data: GraphData): { nodes: FlowNode[]; edges: FlowEdge[] } {
  const nodes = new Map<string, FlowNode>()
  const txIn = new Map<string, { from: string; value: number; height: number; time: number }[]>()
  const txOut = new Map<string, { to: string; value: number; height: number; time: number }[]>()
  const txSeen = new Set<string>()

  for (const n of data.nodes) {
    if (n.type === 'address') {
      nodes.set(n.id, {
        id: n.id,
        kind: 'address',
        label: n.label,
        full: n.id.slice(2),
        value: n.value_btc,
        cex: n.cex,
        cexName: n.cex_name || '',
        terminal: n.terminal || '',
        highlighted: n.cex || !!n.watched || !!n.terminal,
      })
    } else {
      txSeen.add(n.id)
    }
  }
  for (const e of data.edges) {
    if (e.source.startsWith('t:')) {
      txSeen.add(e.source)
      const arr = txOut.get(e.source) || []
      arr.push({ to: e.target, value: e.value_btc, height: e.height || 0, time: e.time || 0 })
      txOut.set(e.source, arr)
    } else {
      const arr = txIn.get(e.target) || []
      if (!arr.some((i) => i.from === e.source)) {
        arr.push({ from: e.source, value: e.value_btc, height: e.height, time: e.time || 0 })
      }
      txIn.set(e.target, arr)
    }
  }

  const edges: FlowEdge[] = []
  for (const tx of txSeen) {
    const txid = tx.slice(2)
    const ins = txIn.get(tx) || []
    const outs = txOut.get(tx) || []
    if (ins.length === 0) {
      nodes.set(tx, {
        id: tx,
        kind: 'tx',
        label: shortTxid(txid),
        full: txid,
        value: 0,
        cex: false,
        cexName: '',
        terminal: '',
        highlighted: data.txids.includes(txid),
      })
      for (const o of outs) {
        edges.push({ id: `${tx}->${o.to}`, source: tx, target: o.to, value: o.value, height: o.height, time: o.time, txid })
      }
    } else if (outs.length === 0) {
      nodes.set(tx, {
        id: tx,
        kind: 'tx',
        label: shortTxid(txid),
        full: txid,
        value: 0,
        cex: false,
        cexName: '',
        terminal: '',
        highlighted: false,
      })
      for (const i of ins) {
        edges.push({ id: `${i.from}->${tx}`, source: i.from, target: tx, value: i.value, height: i.height, time: i.time, txid })
      }
    } else {
      for (const i of ins) {
        for (const o of outs) {
          // change returned to the spending address: skip the self-loop
          if (o.to === i.from) continue
          edges.push({ id: `${i.from}->${o.to}@${tx}`, source: i.from, target: o.to, value: o.value, height: i.height, time: i.time, txid })
        }
      }
    }
  }
  return { nodes: [...nodes.values()], edges }
}

function buildLayout(flow: { nodes: FlowNode[]; edges: FlowEdge[] }): Layout {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 26, ranksep: 300, marginx: 24, marginy: 24 })
  g.setDefaultEdgeLabel(() => ({}))

  for (const n of flow.nodes) {
    const w = n.kind === 'tx' ? 160 : Math.max(210, 56 + n.label.length * 8 + 20)
    const h = n.kind === 'tx' ? 40 : n.cexName ? 64 : 48
    g.setNode(n.id, { width: w, height: h })
  }
  const pairCount = new Map<string, number>()
  for (const e of flow.edges) {
    const key = `${e.source}\u0000${e.target}`
    pairCount.set(key, (pairCount.get(key) || 0) + 1)
  }
  const seenPair = new Set<string>()
  for (const e of flow.edges) {
    const key = `${e.source}\u0000${e.target}`
    if (seenPair.has(key)) continue
    seenPair.add(key)
    g.setEdge(e.source, e.target, {})
  }
  dagre.layout(g)

  const nodes: LaidNode[] = flow.nodes.map((n) => {
    const gn = g.node(n.id) as { x: number; y: number; width: number; height: number }
    return { ...n, x: gn.x, y: gn.y, w: gn.width, h: gn.height }
  })
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const pairIdx = new Map<string, number>()
  const edgesLaid: LaidEdge[] = []
  for (const e of flow.edges) {
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    const key = `${e.source}\u0000${e.target}`
    const i = pairIdx.get(key) || 0
    pairIdx.set(key, i + 1)
    const count = pairCount.get(key) || 1
    const off = (i - (count - 1) / 2) * 14
    const x0 = s.x + s.w / 2
    const y0 = s.y + off
    const x1 = t.x - t.w / 2
    const y1 = t.y + off
    const mx = (x0 + x1) / 2
    const path = `M ${x0} ${y0} C ${mx} ${y0}, ${mx} ${y1}, ${x1 - 8} ${y1}`
    const u = 0.45
    const v = 1 - u
    const bx = v * v * v * x0 + 3 * v * v * u * mx + 3 * v * u * u * mx + u * u * u * x1
    const by =
      v * v * v * y0 + 3 * v * v * u * y0 + 3 * v * u * u * y1 + u * u * u * y1
    const want = edgeLabel(e).length * 6.8
    const fontSize = Math.max(9, Math.min(13, (13 * (x1 - x0 - 20)) / Math.max(want, 1)))
    edgesLaid.push({ ...e, path, lx: bx, ly: by, fontSize })
  }

  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const n of nodes) {
    minX = Math.min(minX, n.x - n.w / 2)
    minY = Math.min(minY, n.y - n.h / 2)
    maxX = Math.max(maxX, n.x + n.w / 2)
    maxY = Math.max(maxY, n.y + n.h / 2)
  }
  const bbox =
    nodes.length > 0
      ? { minX, minY, w: Math.max(1, maxX - minX), h: Math.max(1, maxY - minY) }
      : { minX: 0, minY: 0, w: 1, h: 1 }
  return { nodes, edges: edgesLaid, bbox }
}

interface Detail {
  title: string
  rows: [string, string][]
  link: string
}

function buildDetail(l: Layout, sel: NonNullable<Sel>): Detail | null {
  if (sel.kind === 'edge') {
    const e = l.edges.find((x) => x.id === sel.id)
    if (!e) return null
    return {
      title: 'transaction',
      rows: [
        ['txid', e.txid],
        ['from', stripKind(e.source)],
        ['to', stripKind(e.target)],
        ['amount', fmtBTC(e.value)],
        ['block', e.height > 0 ? String(e.height) : 'unknown'],
        ['time', fmtTime(e.time) || 'unknown'],
      ],
      link: `https://mempool.space/tx/${e.txid}`,
    }
  }
  const n = l.nodes.find((x) => x.id === sel.id)
  if (!n) return null
  if (n.kind === 'address') {
    let sent = 0
    for (const e of l.edges) {
      if (e.source === n.id) sent += e.value
    }
    const rows: [string, string][] = [
      ['address', n.full],
      ['received', fmtBTC(n.value)],
      ['sent', fmtBTC(sent)],
    ]
    if (n.cexName) rows.push(['entity', n.cexName])
    if (n.cex) rows.push(['cex', 'yes'])
    if (n.terminal) rows.push(['terminal', n.terminal])
    return {
      title: 'address',
      rows,
      link: `https://mempool.space/address/${n.full}`,
    }
  }
  const e = l.edges.find((x) => x.txid === n.full)
  return {
    title: 'transaction',
    rows: [
      ['txid', n.full],
      ['block', e && e.height > 0 ? String(e.height) : 'unknown'],
      ['time', fmtTime(e?.time) || 'unknown'],
    ],
    link: `https://mempool.space/tx/${n.full}`,
  }
}

export function GraphView({ data }: { data: GraphData | null }) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const layoutRef = useRef<Layout | null>(null)
  const panRef = useRef<{ px: number; py: number; vx: number; vy: number } | null>(null)
  const movedRef = useRef(false)
  const [layout, setLayout] = useState<Layout | null>(null)
  const [view, setView] = useState({ x: 0, y: 0, k: 1 })
  const [dragging, setDragging] = useState(false)
  const [sel, setSel] = useState<Sel>(null)

  const fit = useCallback(() => {
    const el = wrapRef.current
    const l = layoutRef.current
    if (!el || !l) return
    const cw = el.clientWidth
    const ch = el.clientHeight
    const pad = 70
    const k = Math.min(cw / (l.bbox.w + pad * 2), ch / (l.bbox.h + pad * 2), 1.5) * 0.92
    setView({
      k,
      x: (cw - l.bbox.w * k) / 2 - (l.bbox.minX - pad) * k,
      y: (ch - l.bbox.h * k) / 2 - (l.bbox.minY - pad) * k,
    })
  }, [])

  useEffect(() => {
    if (!data) {
      layoutRef.current = null
      setLayout(null)
      setSel(null)
      return
    }
    const l = buildLayout(collapse(data))
    layoutRef.current = l
    setLayout(l)
    setSel(null)
    requestAnimationFrame(fit)
  }, [data, fit])

  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const onWheel = (ev: WheelEvent) => {
      ev.preventDefault()
      const rect = el.getBoundingClientRect()
      const cx = ev.clientX - rect.left
      const cy = ev.clientY - rect.top
      setView((v) => {
        const k = Math.min(3, Math.max(0.12, v.k * (ev.deltaY < 0 ? 1.1 : 1 / 1.1)))
        const s = k / v.k
        return { k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s }
      })
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  useEffect(() => {
    if (!dragging) return
    const move = (ev: PointerEvent) => {
      const p = panRef.current
      if (!p) return
      const dx = ev.clientX - p.px
      const dy = ev.clientY - p.py
      if (Math.abs(dx) + Math.abs(dy) > 3) movedRef.current = true
      setView((v) => ({ ...v, x: p.vx + dx, y: p.vy + dy }))
    }
    const up = () => {
      panRef.current = null
      setDragging(false)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
  }, [dragging])

  const clickGuard = (fn: () => void) => () => {
    if (!movedRef.current) fn()
  }

  const zoomBy = (f: number) =>
    setView((v) => {
      const el = wrapRef.current
      const cx = (el?.clientWidth || 0) / 2
      const cy = (el?.clientHeight || 0) / 2
      const k = Math.min(3, Math.max(0.12, v.k * f))
      const s = k / v.k
      return { k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s }
    })

  const detail = layout && sel ? buildDetail(layout, sel) : null

  return (
    <div
      className={`graph-canvas${dragging ? ' dragging' : ''}`}
      ref={wrapRef}
      onPointerDown={(e) => {
        movedRef.current = false
        panRef.current = { px: e.clientX, py: e.clientY, vx: view.x, vy: view.y }
        setDragging(true)
      }}
      onDoubleClick={fit}
    >
      <div className="graph-watermark">bitracer</div>
      <svg
        width="100%"
        height="100%"
        onClick={(e) => {
          if (e.target === e.currentTarget) setSel(null)
        }}
      >
        <defs>
          <marker
            id="ms-arrow"
            markerUnits="userSpaceOnUse"
            markerWidth="16"
            markerHeight="14"
            refX="14"
            refY="7"
            orient="auto"
          >
            <path d="M1,1 L15,7 L1,13 Z" fill={ORANGE} />
          </marker>
        </defs>
        {layout && (
          <g transform={`translate(${view.x},${view.y}) scale(${view.k})`}>
            {layout.edges.map((e) => {
              const selected = sel?.kind === 'edge' && sel.id === e.id
              return (
                <g
                  key={e.id}
                  className="edge"
                  onClick={clickGuard(() => setSel({ kind: 'edge', id: e.id }))}
                >
                  <path
                    d={e.path}
                    fill="none"
                    stroke={selected ? '#ffb75e' : ORANGE}
                    strokeWidth={selected ? 4.5 : 3}
                    markerEnd="url(#ms-arrow)"
                  >
                    <title>{`${e.txid}\nblock ${e.height || '?'} · ${
                      fmtTime(e.time) || 'time unknown'
                    } · ${fmtBTC(e.value)}`}</title>
                  </path>
                  <text
                    x={e.lx}
                    y={e.ly - 10}
                    textAnchor="middle"
                    className="graph-edge-label"
                    fontSize={e.fontSize}
                  >
                    {e.height > 0 && <tspan fill={ORANGE}>[{e.height}] </tspan>}
                    {fmtTimeShort(e.time) && <tspan fill={MUTED}>[{fmtTimeShort(e.time)}] </tspan>}
                    <tspan fill={TEXT}>{fmtBTC(e.value)}</tspan>
                  </text>
                </g>
              )
            })}
            {layout.nodes.map((n) => {
              const selected = sel?.kind === 'node' && sel.id === n.id
              return (
                <g
                  key={n.id}
                  className="node"
                  transform={`translate(${n.x - n.w / 2},${n.y - n.h / 2})`}
                  onClick={clickGuard(() => setSel({ kind: 'node', id: n.id }))}
                >
                  <title>{n.full}</title>
                  <rect
                    width={n.w}
                    height={n.h}
                    rx={10}
                    fill={CARD}
                    stroke={
                      selected ? '#ffc37a' : n.highlighted ? ORANGE : CARD_BORDER
                    }
                    strokeWidth={selected ? 2.5 : n.highlighted ? 2 : 1}
                  />
                {n.kind === 'address' ? (
                  <>
                    <circle cx={24} cy={n.h / 2} r={14} fill={ORANGE} />
                    <text
                      x={24}
                      y={n.h / 2 + 1}
                      textAnchor="middle"
                      dominantBaseline="central"
                      fontSize={15}
                      fontWeight={700}
                      fill="#ffffff"
                    >
                      ₿
                    </text>
                    <text
                      x={46}
                      y={n.cexName ? n.h / 2 - 8 : n.h / 2 + 1}
                      dominantBaseline="central"
                      className="graph-addr"
                    >
                      {n.label}
                    </text>
                    {n.cexName && (
                      <text
                        x={46}
                        y={n.h / 2 + 12}
                        dominantBaseline="central"
                        fontSize={11}
                        fontWeight={600}
                        fill={ORANGE}
                      >
                        {n.cexName}
                      </text>
                    )}
                  </>
                ) : (
                  <>
                    <rect x={8} y={n.h / 2 - 11} width={22} height={22} rx={6} fill="#3d4148" />
                    <text
                      x={19}
                      y={n.h / 2 + 1}
                      textAnchor="middle"
                      dominantBaseline="central"
                      fontSize={9}
                      fontWeight={700}
                      fill={MUTED}
                    >
                      TX
                    </text>
                    <text x={38} y={n.h / 2 + 1} dominantBaseline="central" className="graph-addr">
                      {n.label}
                    </text>
                   </>
                 )}
                </g>
              )
            })}
          </g>
        )}
      </svg>
      {detail && (
        <div className="graph-detail">
          <div className="gd-head">
            <span>{detail.title}</span>
            <button onClick={() => setSel(null)}>✕</button>
          </div>
          {detail.rows.map(([k, v]) => (
            <div key={k} className="gd-row">
              <span>{k}</span>
              <b>{v}</b>
            </div>
          ))}
          <a href={detail.link} target="_blank" rel="noreferrer">
            view on mempool.space ↗
          </a>
        </div>
      )}
      {layout && (
        <div className="graph-zoom">
          <button onClick={() => zoomBy(1 / 1.2)}>−</button>
          <span>{Math.round(view.k * 100)}%</span>
          <button onClick={() => zoomBy(1.2)}>+</button>
          <button onClick={fit} title="fit">⤢</button>
        </div>
      )}
    </div>
  )
}
