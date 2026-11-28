#!/usr/bin/env node
/**
 * End-to-end check for the on_error fallback errors against a running nexus service.
 *
 * Scenario A - a handler that fails on its own:
 *   start -> code -> result, the code node fails with `script` (an unparsable template), its
 *   on_error.handler also fails, and on_error.post.set_outputs writes a declared `fixed`.
 *   Expect: failed / err_code script / handler_error set / post_error empty / fixed = "from post"
 *   (the post still runs after the handler failed - that is the rule this scenario protects).
 *
 * Scenario B - a post that fails twice over:
 *   the same failing node, but on_error.post writes an undeclared key and its handler script is broken.
 *   Expect: post_error carries both reasons, in a stable order (write failure, then script failure).
 *
 * Scenario C - the shadowed-handler warning, through the same pre-save check the editor calls:
 *   a node whose skip and handler both list `timeout`. Expect: on_error_shadowed.
 *
 * Usage (PowerShell):
 *   node scripts/e2e-onerror.mjs
 *   $env:TOKEN = "<token copied from the browser>"; node scripts/e2e-onerror.mjs
 *   $env:BASE = "http://localhost:27006"; $env:SCHEMA = "t0"; node scripts/e2e-onerror.mjs
 *
 * Without TOKEN it mints one from `nexus/config.yaml` (jwt.secret_key + issuer) for USER_ID / USER_NAME. That is a
 * local-testing shortcut: minting needs the signing key, which in production nobody has on hand.
 *
 * Prerequisite: the tenant schema must already have flow_node_records.post_error / handler_error - run
 * nexus/sql/flow_node_records_post_handler_error.sql first, otherwise SaveRun fails and every read returns not_found.
 */
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const BASE = process.env.BASE || 'http://localhost:27006'
const SCHEMA = process.env.SCHEMA || 't0'
const USER_NAME = process.env.USER_NAME || 'demo'
const USER_ID = process.env.USER_ID || 'b759b99a-ba85-4972-87ae-21d26bf6ff26'
const STAMP = Date.now()

const here = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(here, '..')

// ---- token ---------------------------------------------------------------------------------------------------------
function mintToken() {
  const cfg = fs.readFileSync(path.join(repoRoot, 'nexus', 'config.yaml'), 'utf8')
  const secret = (cfg.match(/^\s*secret_key:\s*"([^"]+)"/m) || [])[1]
  const issuer = (cfg.match(/^\s*issuer:\s*"([^"]+)"/m) || [])[1] || 'nexus@flame'
  if (!secret || secret.includes('${')) {
    throw new Error('jwt.secret_key in nexus/config.yaml is empty or an env placeholder: set TOKEN instead')
  }
  const b64 = (o) => Buffer.from(JSON.stringify(o)).toString('base64url')
  const now = Math.floor(Date.now() / 1000)
  const head = b64({ alg: 'HS256', typ: 'JWT' })
  const body = b64({ id: USER_ID, name: USER_NAME, schema: SCHEMA, iss: issuer, iat: now, exp: now + 3600 })
  const sig = crypto.createHmac('sha256', secret).update(`${head}.${body}`).digest('base64url')
  return `${head}.${body}.${sig}`
}

const token = (process.env.TOKEN || mintToken()).replace(/^Bearer\s+/i, '')
const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }

const fail = (msg) => {
  console.error(`FAIL  ${msg}`)
  process.exit(1)
}
const ok = (msg) => console.log(`ok    ${msg}`)
const bad = []

async function api(method, p, body) {
  const res = await fetch(`${BASE}${p}`, { method, headers, body: body ? JSON.stringify(body) : undefined })
  const json = await res.json().catch(() => ({}))
  if (json.code !== 200) throw new Error(`${method} ${p} -> ${json.error || json.code}: ${json.msg || res.status}`)
  return json.data
}

async function createFlow(name, diagram, description) {
  const created = await api('POST', '/admin/flows/create', { name, description, diagram, store_runs: true })
  if (!created?.id) fail(`create returned no id: ${JSON.stringify(created)}`)
  ok(`flow created: ${created.id} (${name})`)
  return created.id
}

// The editor's own path: the debug channel, first frame token, then run.
function runDebug(flowID) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${BASE.replace(/^http/, 'ws')}/debug/flow/${flowID}`)
    const timer = setTimeout(() => {
      try { ws.close() } catch {}
      reject(new Error('timed out after 30s without a finished frame'))
    }, 30000)
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
        const rid = id || d.run?.id || null
        try { ws.close() } catch {}
        if (!rid) reject(new Error('no run_id in run_started / finished'))
        else resolve(rid)
      } else if (d.event === 'error') {
        clearTimeout(timer)
        try { ws.close() } catch {}
        reject(new Error(`debug channel error: ${d.error}`))
      }
    }
  })
}

async function readNode(flowID, runID, nodeID) {
  const detail = await api('GET', `/admin/flows/${flowID}/runs/${runID}`)
  const node = (detail.nodes || []).find((n) => n.node_id === nodeID)
  if (!node) fail(`node ${nodeID} is missing from the run detail: ${JSON.stringify((detail.nodes || []).map((n) => n.node_id))}`)
  return node
}

function assertAll(checks) {
  for (const [pass, msg] of checks) {
    if (pass) ok(msg)
    else { console.error(`FAIL  ${msg}`); bad.push(msg) }
  }
}

// ---- diagrams ------------------------------------------------------------------------------------------------------
const shell = (middle) => ({
  nodes: [
    { id: '1', type: 'start', data: { label: 'start' }, position: { x: 100, y: 60 } },
    middle,
    {
      id: '3',
      type: 'result',
      data: { label: 'result', config: { code: { lang: 'template', script: 'done' }, stream: false } },
      position: { x: 540, y: 60 },
    },
  ],
  edges: [
    { id: 'e1', source: '1', target: '2', source_handle: 'source1right', target_handle: 'target2left' },
    { id: 'e2', source: '2', target: '3', source_handle: 'source2right', target_handle: 'target3left' },
  ],
})

// A: the node fails, its handler fails too, and post writes a declared value.
const diagramA = shell({
  id: '2',
  type: 'code',
  data: {
    label: 'boom',
    config: {
      code: { lang: 'template', script: '{{' },
      outputs: { fixed: { type: 'string' } },
      on_error: {
        handler: { codes: ['script'], lang: 'template', script: '{{' },
        post: { set_outputs: { fixed: 'from post' } },
      },
    },
  },
  position: { x: 320, y: 60 },
})

// B: the same failure, but post itself breaks twice: an undeclared key and a broken script.
const diagramB = shell({
  id: '2',
  type: 'code',
  data: {
    label: 'boom + broken post',
    config: {
      code: { lang: 'template', script: '{{' },
      outputs: { fixed: { type: 'string' } },
      on_error: {
        post: { set_outputs: { missing: 'x' }, handler: { lang: 'template', script: '{{' } },
      },
    },
  },
  position: { x: 320, y: 60 },
})

// ---- A: handler failure, post still runs ---------------------------------------------------------------------------
console.log('\n--- A: handler fails, post still writes ---')
const flowA = await createFlow(`onerror-e2e-a-${STAMP}`, diagramA, 'e2e: handler failure + post marker')
const runA = await runDebug(flowA)
ok(`run finished: ${runA}`)
const nodeA = await readNode(flowA, runA, '2')
const outA = nodeA.output_vars || {}
assertAll([
  [nodeA.status === 'failed', `node status = ${nodeA.status} (want failed)`],
  [nodeA.err_code === 'script', `err_code = ${nodeA.err_code} (want script: the original code is kept)`],
  [!!nodeA.handler_error, `handler_error = ${JSON.stringify(nodeA.handler_error)}`],
  [!nodeA.post_error, `post_error = ${JSON.stringify(nodeA.post_error)} (want empty: post itself is fine)`],
  [outA.fixed === 'from post', `output_vars.fixed = ${JSON.stringify(outA.fixed)} (want the post marker written)`],
])

// ---- B: post fails twice over --------------------------------------------------------------------------------------
console.log('\n--- B: post fails on an undeclared key and on its script ---')
const flowB = await createFlow(`onerror-e2e-b-${STAMP}`, diagramB, 'e2e: post write failure + post script failure')
const runB = await runDebug(flowB)
ok(`run finished: ${runB}`)
const nodeB = await readNode(flowB, runB, '2')
assertAll([
  [nodeB.status === 'failed', `node status = ${nodeB.status} (want failed)`],
  [(nodeB.post_error || '').includes('failed to write post output missing'), `post_error keeps the write failure: ${JSON.stringify(nodeB.post_error)}`],
  [(nodeB.post_error || '').includes('template'), `post_error keeps the script failure too: ${JSON.stringify(nodeB.post_error)}`],
  [!nodeB.handler_error, `handler_error = ${JSON.stringify(nodeB.handler_error)} (want empty: no handler configured)`],
])

// ---- C: the shadowed handler warning -------------------------------------------------------------------------------
console.log('\n--- C: skip shadows handler ---')
const verdict = await api('POST', '/admin/flows/validate', {
  diagram: {
    nodes: [
      { id: '1', type: 'start', data: { label: 'start' } },
      {
        id: '2',
        type: 'code',
        data: {
          label: 'shadowed',
          config: {
            code: { lang: 'template', script: 'ok' },
            on_error: {
              skip: { codes: ['timeout'] },
              handler: { codes: ['timeout'], lang: 'template', script: 'skip' },
            },
          },
        },
      },
    ],
    edges: [{ id: 'e1', source: '1', target: '2' }],
  },
})
const shadowed = (verdict.findings || []).filter((f) => f.code === 'on_error_shadowed')
assertAll([
  [shadowed.length > 0, shadowed.length ? `validate reports on_error_shadowed: ${shadowed[0].message}` : `validate did not report on_error_shadowed: ${JSON.stringify(verdict.findings)}`],
])

console.log(`\nflows: A=${flowA} B=${flowB}`)
console.log(`runs : A=${runA} B=${runB}`)
if (bad.length) process.exit(1)
console.log('E2E PASSED')
