import { useEffect, useRef } from 'react'
import cytoscape, { type Core, type ElementDefinition } from 'cytoscape'
import type { GraphData } from './api'

export function GraphView({ data }: { data: GraphData | null }) {
  const ref = useRef<HTMLDivElement>(null)
  const cyRef = useRef<Core | null>(null)

  useEffect(() => {
    if (!ref.current) return
    const cy = cytoscape({
      container: ref.current,
      style: [
        {
          selector: 'node[type = "tx"]',
          style: {
            shape: 'round-rectangle',
            'background-color': '#3b4252',
            label: 'data(label)',
            color: '#d8dee9',
            'font-size': 9,
            'text-valign': 'center',
            'text-halign': 'center',
            width: 66,
            height: 26,
          },
        },
        {
          selector: 'node[type = "address"]',
          style: {
            shape: 'ellipse',
            'background-color': '#434c5e',
            'border-width': 2,
            'border-color': '#5b8def',
            label: 'data(label)',
            color: '#eceff4',
            'font-size': 9,
            'text-valign': 'bottom',
            'text-halign': 'center',
            width: 34,
            height: 34,
          },
        },
        {
          selector: 'node[cex = true]',
          style: {
            'background-color': '#e5484d',
            'border-color': '#febc2e',
            width: 44,
            height: 44,
          },
        },
        {
          selector: 'edge',
          style: {
            width: '2px',
            'line-color': '#7b88a1',
            'target-arrow-color': '#7b88a1',
            'target-arrow-shape': 'triangle',
            'curve-style': 'bezier',
            label: 'data(label)',
            'font-size': 8,
            color: '#9aa5b8',
            'text-background-color': '#2e3440',
            'text-background-opacity': 1,
            'text-background-padding': '2px',
          },
        },
      ],
      layout: { name: 'cose', animate: true, padding: 30 },
    })
    cyRef.current = cy
    return () => cy.destroy()
  }, [])

  useEffect(() => {
    const cy = cyRef.current
    if (!cy || !data) return
    const els: ElementDefinition[] = [
      ...data.nodes.map((n) => ({
        data: {
          id: n.id,
          type: n.type,
          label: n.cex ? `${n.label}\n[${n.cex_name || 'CEX'}]` : n.label,
          cex: n.cex,
        },
      })),
      ...data.edges.map((e) => ({
        data: {
          id: e.id,
          source: e.source,
          target: e.target,
          label: `${e.value_btc.toFixed(4)} BTC`,
        },
      })),
    ]
    cy.elements().remove()
    cy.add(els)
    cy.layout({ name: 'cose', animate: true, padding: 30 }).run()
  }, [data])

  return <div ref={ref} style={{ flex: 1, minHeight: 500, background: '#2e3440', borderRadius: 8 }} />
}
