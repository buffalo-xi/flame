#!/usr/bin/env node
/**
 * End-to-end check of one contract: `exit` fired from a **concurrent member** ends the whole run, successfully.
 *
 * Flow:
 *   start(1) -> process(2) -> result(3)
 *                  body: entry -> x1 (concurrent `exit`) -> m2 (concurrent `code`)
 *
 * Expected, all read back from the stored records:
 *   - the run status is `success` and `stop_reason` is `exit` with `stop_node_id` = x1;
 *   - the host node 2 is `success` (the signal is control flow, not a failure);
 *   - node 3 (the `result` after the host) never ran: an exit in a member stops the run, it does not stop only the body;
 *   - nothing is reported as failed.
 *
 * Usage (PowerShell):
 *   node scripts/e2e-exit-member.mjs
 *   $env:TOKEN = "<token from the browser>"; node scripts/e2e-exit-member.mjs
 *   $env:BASE = "http://localhost:27006"; $env:SCHEMA = "t0"; node scripts/e2e-exit-member.mjs
 *
 * Without TOKEN it mints one from `nexus/config.yaml` (jwt.secret_key + issuer) for USER_ID / USER_NAME: a local-testing
 * shortcut, since minting needs the signing key.
 */
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const BASE = process.env.BASE || 'http://localhost:27006'
const SCHEMA = process.env.SCHEMA || 't0'
const USER_NAME = process.env.USER_NAME || 'demo'
const USER_ID = process.env.USER_ID || 'b759b99a-ba85-4972-87ae-21d26bf6ff26'

const here = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(here, '..')

function mintToken() {
  const cfg = fs.readFileSync(path.join(repoRoot, 'nexus', 'config.yaml'), 'utf8')
  const secret = (cfg.match(/^\s*secret_key:\s*"([^"]+)"/m) || [])[1]
  const issuer = (cfg.match(/^\s*issuer:\s*"([^"]+)"/m) || [])[1] || 'nexus@flame'
  if (!secret || secret.includes('${')) throw new Error('jwt.secret_key is empty or an env placeholder: set TOKEN')
  const b64 = (o) => Buffer.from(JSON.stringify(o)).toString('base64url')
  const now = Math.floor(Date.now() / 1000)
  const head = b64({ alg: 'HS256', typ: 'JWT' })
  const body = b64({ id: USER_ID, name: USER_NAME, schema: SCHEMA, iss: issuer, iat: now, exp: now + 3600 })
  return `${head}.${body}.${crypto.createHmac('sha256', secret).update(`${head}.${body}`).digest('base64url')}`
}

const token = (process.env.TOKEN || mintToken()).replace(/^Bearer\s+/i, '')
const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }

async function api(method, p, body) {
  const res = await fetch(`${BASE}${p}`, { method, headers, body: body ? JSON.stringify(body) : undefined })
  const json = await res.json().catch(() => ({}))
  if (json.code !== 200) throw new Error(`${method} ${p} -> ${json.error || json.code}: ${json.msg || res.status}`)
  return json.data
}

const diagram = {
  nodes: [
    { id: '1', type: 'start', data: { label: 'start' }, position: { x: 80, y: 80 } },
    {
      id: '2',
      type: 'process',
      data: { label: 'scope', config: { outputs: {} } },
      position: { x: 300, y: 80 },
    },
    {
      id: '2-entry',
      type: 'entry',
      parent_node: '2',
      data: { label: 'Entry' },
      position: { x: 200, y: 200 },
    },
    // The member is a nested host whose body fires the exit: an `exit` node is never a member itself (control flow lives
    // on a chain, and flowcheck refuses the flag on it), but the signal it produces is global.
    {
      id: 'p2',
      type: 'process',
      parent_node: '2',
      data: { label: 'member body', concurrent: true, config: { outputs: {} } },
      position: { x: 380, y: 200 },
    },
    {
      id: 'p2-entry',
      type: 'entry',
      parent_node: 'p2',
      data: { label: 'Entry' },
      position: { x: 20, y: 40 },
    },
    {
      id: 'x1',
      type: 'exit',
      parent_node: 'p2',
      data: { label: 'stop everything' },
      position: { x: 200, y: 40 },
    },
    {
      id: 'm2',
      type: 'code',
      parent_node: '2',
      data: {
        label: 'sibling',
        concurrent: true,
        // the code node parses the rendered template as JSON and refuses undeclared output variables
        config: {
          code: { lang: 'template', script: '{"ok":true}' },
          outputs: { ok: { type: 'boolean' } },
        },
      },
      position: { x: 560, y: 200 },
    },
    {
      id: '3',
      type: 'result',
      data: { label: 'result', config: { code: { lang: 'template', script: 'done' }, stream: false } },
      position: { x: 560, y: 80 },
    },
  ],
  edges: [
    { id: 'e1', source: '1', target: '2', source_handle: 'source1right', target_handle: 'target2left' },
    { id: 'e2', source: '2', target: '3', source_handle: 'source2right', target_handle: 'target3left' },
    { id: 'e3', source: '2-entry', target: 'p2', source_handle: 'sourceEntry', target_handle: 'targetP2' },
    { id: 'e4', source: 'p2', target: 'm2', source_handle: 'sourceP2', target_handle: 'targetM2' },
    { id: 'e5', source: 'p2-entry', target: 'x1', source_handle: 'sourceEntry', target_handle: 'targetX1' },
  ],
}

const flow = await api('POST', '/admin/flows/create', {
  name: `exit-member-e2e-${Date.now()}`,
  description: 'e2e: a concurrent member fires exit (scripts/e2e-exit-member.mjs)',
  diagram,
  store_runs: true,
})
const flowID = flow?.id
if (!flowID) {
  console.error('FAIL  create returned no id:', JSON.stringify(flow))
  process.exit(1)
}
console.log(`ok    flow created: ${flowID}`)

const runID = await new Promise((resolve, reject) => {
  const ws = new WebSocket(`${BASE.replace(/^http/, 'ws')}/debug/flow/${flowID}`)
  const timer = setTimeout(() => reject(new Error('timed out after 30s waiting for finished')), 30000)
  let id = null
  ws.onopen = () => ws.send(JSON.stringify({ token }))
  ws.onerror = () => {}
  ws.onmessage = (e) => {
    let d
    try { d = JSON.parse(e.data) } catch { return }
    if (d.event === 'ready') {
      ws.send(JSON.stringify({ action: 'run', input: {}, follow_drafts: true }))
    } else if (d.event === 'run_started') {
      id = d.run_id || null
    } else if (d.event === 'finished') {
      clearTimeout(timer)
      try { ws.close() } catch {}
      resolve(id || d.run?.id)
    } else if (d.event === 'error') {
      clearTimeout(timer)
      try { ws.close() } catch {}
      reject(new Error(`debug channel error: ${d.error}`))
    }
  }
})
if (!runID) {
  console.error('FAIL  no run_id')
  process.exit(1)
}
console.log(`ok    run finished: ${runID}`)

const detail = await api('GET', `/admin/flows/${flowID}/runs/${runID}`)
const nodes = detail.nodes || []
const run = detail.run || {}
const byID = Object.fromEntries(nodes.map((n) => [n.node_id, n]))
const bad = []
const check = (pass, msg) => (pass ? console.log(`ok    ${msg}`) : (console.error(`FAIL  ${msg}`), bad.push(msg)))

// The admin run detail has no stop_reason (that derivation lives in the agent's read_flow_run); the evidence here is the
// successful exit node plus the node that never ran, which is exactly what runStopSummary reads.
check(run.status === 'success', `run status = ${run.status} (want success: exit is control flow)`)
check(byID['2']?.status === 'success', `host node 2 status = ${byID['2']?.status} (want success)`)
check(byID['p2']?.status === 'success',
  `member node p2 status = ${byID['p2']?.status} (want success: the member carried the exit out)`)
check(byID['x1']?.node_type === 'exit' && byID['x1']?.status === 'success',
  `exit node = ${byID['x1']?.node_type}/${byID['x1']?.status} (want exit/success: the proof runStopSummary reads)`)
check(!byID['3'], `node 3 present = ${!!byID['3']} (want false: nothing after the host runs)`)
const failed = nodes.filter((n) => n.status === 'failed')
check(failed.length === 0, `failed nodes = ${JSON.stringify(failed.map((n) => n.node_id))} (want none)`)

console.log(`\nflow_id=${flowID}\nrun_id=${runID}`)
console.log('nodes:')
for (const n of nodes) {
  console.log(`  ${n.node_id.padEnd(8)} ${String(n.node_type).padEnd(8)} ${String(n.status).padEnd(9)} ${n.err_code || ''} ${(n.err_msg || '').slice(0, 90)}`)
}
if (bad.length) process.exit(1)
console.log('EXIT-MEMBER E2E PASSED')
