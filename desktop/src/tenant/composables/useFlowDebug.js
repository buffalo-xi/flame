import { ref, reactive } from 'vue'
import { getToken, getAuthHeader } from '@/tenant/utils/auth.js'
import { apiBase } from '@/tenant/config/base.js'
import { runInfo, nodeResults, fillRunInfo, setNodeResult, clearNodeResults } from './runData.js'

/**
 * @typedef {Object} DebugEvent
 * @property {string} type
 * @property {string} kind
 * @property {string} event      ready|suspended|resumed|control|stopped|inspect|breakpoint|error
 * @property {string} [node_id]
 * @property {string} [node_type]
 * @property {number} [depth]
 * @property {string[]} [stack]
 * @property {any} [run_args]
 * @property {any} [flow_vars]
 */

/**
 * useFlowDebug —— 对接后端 /debug/flow/:flowId WS 调试通道的 composable。
 *
 * 设计要点：
 * 1. connected 仅由后端推来的 ready 帧置 true（非 ws open），避免盲发控制帧。
 * 2. 用户操作先改本地 breakpoints（UI 即时正确），再按需同步后端：
 *    - 已连：立即单条同步；
 *    - 未连：只存本地，等 ready 后全量补发。
 * 3. 控制方法（run/pause/step）在未连时先 connect，发出的帧进入 pending，
 *    ready 后由 flushPending 自动补发。
 * 4. 不直接触碰 nodes 数组（高亮/红点由 Flow.vue 用 updateNode / 节点组件自渲染）。
 */
export function useFlowDebug() {
  const connected = ref(false)
  const running = ref(false)
  const mode = ref('idle')               // idle|run|pause|step
  // 本次调试调起的子流程读哪一份：true=草稿（默认，和「调试就是看未发布内容」一致），
  // false=正式版本（用来确认线上链路）。主流程始终是画布上的草稿，不受此开关影响。
  const followDrafts = ref(true)
  const highlightNodeId = ref('')        // 当前挂起/执行节点（供 Flow.vue 应用高亮）
  const highlightStack = ref([])         // 调用栈 node_id 链
  const breakpoints = reactive(new Set()) // 断点 node_id 集合（本地真相源）
  const variables = ref(null)            // inspect 结果
  const logs = ref([])
  const result = ref('')                  // 工作流最终返回信息
  const error = ref('')
  const runs = ref([])                    // 每次「全新运行」一个卡片：{ id, startTime, input, logs, progress, result, finalResult, error, status }
  let currentRunId = null                 // 当前卡片 id（过程日志/进度/结果写入目标）
  const visitedNodeIds = ref([])          // 已运行节点 id 列表（累积，run 时清空）
  const visitedTick = ref(0)              // 触发 Flow.vue 增量 watch 的计数器
  const failedNodeIds = ref([])           // 出错节点 id 列表（累积，run 时清空）
  const failedTick = ref(0)               // 触发 Flow.vue 增量 watch 的计数器
  const skippedNodeIds = ref([])          // 跳过节点 id 列表（累积，run 时清空）
  const skippedTick = ref(0)              // 触发 Flow.vue 增量 watch 的计数器
  let ws = null
  let flowId = ''
  let pending = []                      // ready 前暂存的控制帧

  function wsBase() {
    // apiBase 是相对路径（如 /nexus），前端无需感知后端 IP：
    // WS 与 HTTP 同源发出，由 Vite(proxy.ws:true) 或 nginx 剥离 /nexus 前缀后转发到后端 /debug/...
    const origin = window.location.origin
    return origin.replace(/^http/, 'ws') + apiBase
  }

  // 生成节点标签「名字（id）」；名字为空时退化为「id」
  function nodeLabel(d) {
    const name = d.node_name || ''
    const id = d.node_id || ''
    if (name && id) return `${name}（${id}）`
    if (name) return name
    return id
  }

  function pushLog(m, type) {
    const line = `[${new Date().toLocaleTimeString()}] ${m}`
    // 有当前卡片则写入卡片（供试运行窗口卡片展示），同时写全局兜底
    if (currentRunId) {
      const run = runs.value.find((r) => r.id === currentRunId)
      if (run) {
        run.logs.push({ time: new Date().toLocaleTimeString(), message: m, type: type || 'info' })
        if (run.logs.length > 200) run.logs.shift()
      }
    }
    logs.value.push(line)
    if (logs.value.length > 200) logs.value.shift()
  }

  function rawSend(obj) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(obj))
      return true
    }
    return false
  }

  // 已连：直接发；未连：暂存（等 ready 补发）
  function send(obj) {
    if (connected.value) return rawSend(obj)
    pending.push(obj)
    return false
  }

  // ready 后补发 pending 控制帧 + 全量断点
  function flushPending() {
    pending.forEach(rawSend)
    pending = []
    breakpoints.forEach((id) => rawSend({ action: 'setBreakpoint', breakpoint: id, on: true }))
    pushLog(`已同步断点 ${breakpoints.size} 个`)
  }

  function handleFrame(d) {
    switch (d.event) {
      case 'ready':
        connected.value = true
        pushLog('调试通道就绪 ' + flowId)
        flushPending()
        break
      case 'run_started':
        // 运行启动即填充运行级信息，供断点挂起时查看「开始节点」的入参/限额
        fillRunInfo({
          ...runInfo.value,
          id: d.run_id || null,
          flow_id: flowId || null,
          status: 'running',
          run_type: 'debug',
          started_at: d.started_at || null,
          run_args: d.run_args ?? null,
          run_limits: d.run_limits ?? null,
        })
        break
      case 'node_executing':
        // 节点开始执行：累积到已运行集合（去重），并 tick 触发 Flow.vue 增量高亮
        if (d.node_id && !visitedNodeIds.value.includes(d.node_id)) {
          visitedNodeIds.value.push(d.node_id)
          visitedTick.value++
        }
        // 当前执行节点：设为运行态（右上角旋转 loading 图标）
        if (d.node_id) {
          highlightNodeId.value = d.node_id
          highlightStack.value = []
          mode.value = 'run'
        }
        break
      case 'node_failed':
        // 节点执行失败：累积到出错集合（去重），并 tick 触发 Flow.vue 增量标记
        if (d.node_id && !failedNodeIds.value.includes(d.node_id)) {
          failedNodeIds.value.push(d.node_id)
          failedTick.value++
          pushLog('节点执行失败: ' + nodeLabel(d) + ' ' + (d.error || ''), 'error')
        }
        // 失败即完成：取消该节点的运行态（旋转图标），改由 debug-error 呈现结果
        if (d.node_id && highlightNodeId.value === d.node_id) {
          highlightNodeId.value = ''
          highlightStack.value = []
        }
        break
      case 'node_skipped':
        // 节点被跳过：累积到跳过集合（去重），并 tick 触发 Flow.vue 增量标记。
        // 同时从出错集合移除（节点先 Finish(failed) 触发 failHook，后被 on_error 降级为 skip，
        // 最终状态应为 skipped 而非 error，需移除先前的 error 标记）。
        if (d.node_id) {
          if (!skippedNodeIds.value.includes(d.node_id)) {
            skippedNodeIds.value.push(d.node_id)
            skippedTick.value++
            pushLog('节点已跳过: ' + nodeLabel(d))
          }
          if (failedNodeIds.value.includes(d.node_id)) {
            failedNodeIds.value = failedNodeIds.value.filter((id) => id !== d.node_id)
            failedTick.value++
          }
          // 跳过即完成：取消该节点的运行态（旋转图标），改由 debug-skipped 呈现结果
          if (highlightNodeId.value === d.node_id) {
            highlightNodeId.value = ''
            highlightStack.value = []
          }
        }
        break
      case 'suspended':
        highlightNodeId.value = d.node_id || ''
        highlightStack.value = d.stack || []
        mode.value = 'pause'
        break
      case 'resumed':
      case 'control':
        if (d.mode) mode.value = d.mode
        break
      case 'stopped':
        running.value = false
        mode.value = 'idle'
        highlightNodeId.value = ''
        highlightStack.value = []
        connected.value = false
        break
      case 'progress':
        // 流式进度：连续拼接（消息之间不分行）
        if (currentRunId && d.message) {
          const run = runs.value.find((r) => r.id === currentRunId)
          if (run) run.progress += d.message
        }
        break
      case 'result_stream':
        // 流式结果：连续拼接（消息之间不分行）
        if (currentRunId && d.message) {
          const run = runs.value.find((r) => r.id === currentRunId)
          if (run) run.result += d.message
        }
        break
      case 'finished':
        // 流程执行结束（成功/失败/被取消均会推此帧）。后端在推完此帧后主动关闭通道，
        // 前端同步将 connected 置 false 并主动关闭 ws（随后 onclose 也会触发一次，幂等）。
        running.value = false
        mode.value = 'idle'
        connected.value = false
        highlightNodeId.value = ''
        highlightStack.value = []
        result.value = d.result || ''
        fillRunInfo(d.run || null)
        // 写入当前卡片的状态与最终结果
        if (currentRunId) {
          const run = runs.value.find((r) => r.id === currentRunId)
          if (run) {
            run.error = d.error || ''
            run.status = d.error ? 'failed' : 'finished'
            run.endTs = Date.now()
            if (d.result) run.finalResult = d.result
          }
        }
        if (d.error) {
          error.value = d.error
          pushLog('执行失败: ' + d.error, 'error')
        } else {
          if (result.value) {
            pushLog('返回结果: ' + result.value, 'result')
          }
          pushLog('执行完成')
        }
        // 运行结束主动关闭调试通道（与后端 pushFinished 关闭互为兜底）
        if (ws) {
          try { ws.close() } catch (e) { /* ignore */ }
        }
        break
      case 'inspect':
        variables.value = { run_args: d.run_args, flow_vars: d.flow_vars }
        break
      case 'node_inspect':
      case 'node_snapshot':
        // 单节点快照：按 node_id 覆盖（保留最后一次执行的数据，与后端去重语义一致）
        setNodeResult(d.node)
        // 快照为成功时收回错误红框：重试成功等"失败被成功覆盖"的场景，
        // 与 node_skipped 移除 failed 标记的逻辑对齐，避免画布残留失败标记
        if (d.node?.status === 'success' && failedNodeIds.value.includes(d.node.node_id)) {
          failedNodeIds.value = failedNodeIds.value.filter((id) => id !== d.node.node_id)
          failedTick.value++
        }
        break
      case 'error':
        error.value = d.error || '未知错误'
        // 错误信息写入当前卡片（若有），标记失败状态（结束卡片会红色展示 run.error）
        if (currentRunId) {
          const run = runs.value.find((r) => r.id === currentRunId)
          if (run) {
            run.error = error.value
            run.status = 'failed'
            run.endTs = Date.now()
          }
        }
        // 错误日志用 error 类型，红色显示在「执行」区
        pushLog('错误: ' + error.value, 'error')
        // 停止调试通道：复位运行状态并主动关闭 ws
        running.value = false
        mode.value = 'idle'
        connected.value = false
        highlightNodeId.value = ''
        highlightStack.value = []
        if (ws) {
          try { ws.close() } catch (e) { /* ignore */ }
        }
        break
      case 'breakpoint':
        // setBreakpoint 的应答帧（已设置/取消断点），对用户无意义，静默跳过
        break
      default:
        // event 为空（如后端 welcome 欢迎帧，只有 type 无 event）时静默跳过
        if (d.event) pushLog('帧: ' + d.event)
    }
  }

  function connect(id) {
    flowId = id
    if (ws) {
      try { ws.close() } catch (e) { /* ignore */ }
    }
    ws = new WebSocket(`${wsBase()}/debug/flow/${id}`)
    ws.onopen = () => {
      // 先发鉴权帧（后端收此帧校验后推 ready）
      rawSend({ token: (getToken() || '').replace(/^Bearer\s+/i, '') })
    }
    ws.onmessage = (e) => {
      try {
        handleFrame(JSON.parse(e.data))
      } catch (err) {
        pushLog('帧解析失败: ' + err.message, 'error')
      }
    }
    ws.onclose = () => {
      connected.value = false
      running.value = false
      // mode 必须跟着复位：正常结束有 'finished' / 'stopped' 帧先把它归到 idle，
      // 但通道异常断开（后端超时、被关、网络断）不会来这两帧。留在 'pause' 会让
      // 下一次点「运行」被当成「继续」—— 既不新建运行卡片，也跳过运行前的自动保存。
      mode.value = 'idle'
      highlightNodeId.value = ''
      highlightStack.value = []
      ws = null
      pushLog('调试通道已关闭')
    }
    ws.onerror = () => {
      error.value = 'WS 连接错误'
      // 通道级失败也算「执行中的错误」：用 error 类型落日志，执行区才会标红
      pushLog('WS 错误', 'error')
    }
  }

  function disconnect() {
    send({ action: 'stop' })
    if (ws) {
      try { ws.close() } catch (e) { /* ignore */ }
      ws = null
    }
    connected.value = false
    running.value = false
    // 重置运行模式，确保下次「运行」按全新运行处理（新建卡片），而非复用被取消的卡片
    mode.value = 'idle'
    // 用户主动终止：把当前运行中的卡片标记为已取消，使其收缩成 tab 样式
    if (currentRunId) {
      const run = runs.value.find((r) => r.id === currentRunId)
      if (run && run.status === 'running') {
        run.status = 'cancelled'
        run.endTs = Date.now()
        pushLog('已终止')
      }
    }
  }

  // 清空所有运行卡片
  function clearRuns() {
    runs.value = []
    currentRunId = null
  }

  // 打开/切换流程时重置运行态。runInfo 与 nodeResults 是模块级单例（跨组件/流程共享），
  // 不显式清空会导致打开新流程时残留上一流程的运行结果。
  function resetRunData() {
    fillRunInfo(null)
    clearNodeResults()
    clearRuns()
    result.value = ''
    error.value = ''
    logs.value = []
    visitedNodeIds.value = []
    visitedTick.value++
    failedNodeIds.value = []
    failedTick.value++
    skippedNodeIds.value = []
    skippedTick.value++
  }

  // 历史记录回填时，同步画布节点运行状态（清空 + 按 nodes 的 status 重设）。
  // 与调试一致：visited=执行过的节点，failed=失败，skipped=跳过。
  function applyHistoryNodeStates(nodes) {
    const visited = []
    const failed = []
    const skipped = []
    ;(nodes || []).forEach((n) => {
      if (!n.node_id) return
      visited.push(n.node_id)
      if (n.status === 'failed') failed.push(n.node_id)
      else if (n.status === 'skipped') skipped.push(n.node_id)
    })
    visitedNodeIds.value = visited
    failedNodeIds.value = failed
    skippedNodeIds.value = skipped
    visitedTick.value++
    failedTick.value++
    skippedTick.value++
  }

  // 控制方法：未连则先 connect（建连后由 ready 触发 flushPending 自动发出）
  // run 必须传入 flowId（来自 RunPanel 的 props.flowId），因为连接是按需（点运行时）建立的，
  // 此前内部 flowId 可能为空。
  function run(input, id) {
    running.value = true
    result.value = ''
    // 仅「全新运行」时清空上一轮轨迹；「断点继续」（mode==='pause'）时保留，
    // 否则点继续会把之前已运行节点的边框清掉。
    if (mode.value !== 'pause') {
      // 新一轮运行：清空上一轮的已运行轨迹，tick 触发 Flow.vue 移除旧高亮
      visitedNodeIds.value = []
      visitedTick.value++
      // 同时清空上一轮的出错标记
      failedNodeIds.value = []
      failedTick.value++
      // 同时清空上一轮的跳过标记
      skippedNodeIds.value = []
      skippedTick.value++
      // 清空上一轮的节点数据快照
      clearNodeResults()
      // 重置上一轮的运行级信息（否则新一轮断点挂起时「开始节点」会显示旧入参）
      fillRunInfo(null)
      // 新建一个运行卡片（输入信息 + 过程/进度/结果）
      currentRunId = 'run-' + Date.now()
      runs.value.push({
        id: currentRunId,
        startTime: new Date().toLocaleTimeString(),
        startTs: Date.now(),
        input: input || {},
        logs: [],
        progress: '',
        result: '',
        finalResult: '',
        error: '',
        status: 'running'
      })
    }
    if (id) flowId = id
    if (!connected.value) connect(flowId)
    // 作用域只在「全新运行」时由后端读取（已启动的继续运行不会重建 scope）
    send({ action: 'run', input: input || {}, follow_drafts: followDrafts.value })
  }
  function pause(id) {
    if (id) flowId = id
    if (!connected.value) connect(flowId)
    send({ action: 'pause' })
  }
  function step(kind = 'over', id) {
    if (id) flowId = id
    if (!connected.value) connect(flowId)
    send({ action: 'step', step_kind: kind })
  }
  function stop() { disconnect() }
  // 查看流程变量（仅调试运行中：ws 已连接才拉取，返回 inspect 帧）。
  // ws 断开后不再拉取，也不进 pending。
  function inspect() {
    if (!connected.value) return
    rawSend({ action: 'inspect' })
  }
  // 点节点查看该节点的执行数据（仅调试运行中：ws 已连接才拉取，返回 node_inspect 帧）。
  // ws 断开后（调试结束/历史查看）不再拉取，也不进 pending，避免下次连接补发过期查询。
  function inspectNode(nodeId) {
    if (!nodeId || !connected.value) return
    rawSend({ action: 'inspect_node', node_id: nodeId })
  }

  // 注入 flowId（Flow.vue 初始化时调用，此后 toggleBreakpoint 可即时落库）
  function setFlowId(id) {
    if (id) flowId = id
  }

  // 打开工作流时加载已保存断点，填充本地 Set（触发红点）
  async function loadBreakpoints(id) {
    if (!id) return
    try {
      const resp = await fetch(`${apiBase}/admin/flows/${id}/breakpoints`, {
        headers: { ...getAuthHeader() }
      })
      const data = await resp.json()
      if (data.code === 200 && Array.isArray(data.data?.breakpoints)) {
        breakpoints.clear()
        data.data.breakpoints.forEach((nid) => nid && breakpoints.add(nid))
      }
    } catch (e) { /* ignore */ }
  }

  // 返回当前断点数组（保存工作流时拼入请求体，全量覆盖）
  function breakpointIds() {
    return [...breakpoints]
  }

  // 单条断点即时落库（仅已保存工作流；未保存无 flowId 跳过）
  async function persistBreakpoint(nodeId, on) {
    try {
      const resp = await fetch(`${apiBase}/admin/flows/${flowId}/breakpoints`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...getAuthHeader() },
        body: JSON.stringify({ node_id: nodeId, on })
      })
      const data = await resp.json()
      if (data.code !== 200) console.warn('断点持久化失败:', data.msg)
    } catch (e) {
      console.warn('断点持久化失败:', e?.message || e)
    }
  }

  // 断点：本地先更新（UI 即时）+ WS 同步（运行时命中）+ 即时落库（防刷新丢）
  function toggleBreakpoint(nodeId) {
    if (!nodeId) return
    const on = !breakpoints.has(nodeId)
    if (on) breakpoints.add(nodeId)
    else breakpoints.delete(nodeId)
    send({ action: 'setBreakpoint', breakpoint: nodeId, on })
    if (flowId) persistBreakpoint(nodeId, on)
  }
  function isBreakpoint(nodeId) { return breakpoints.has(nodeId) }

  // 批量移除断点（删除节点时清理本地 + WS 同步 + 落库）
  function removeBreakpoints(ids) {
    if (!ids || !ids.length) return
    ids.forEach((nodeId) => {
      if (breakpoints.has(nodeId)) {
        breakpoints.delete(nodeId)
        send({ action: 'setBreakpoint', breakpoint: nodeId, on: false })
        if (flowId) persistBreakpoint(nodeId, false)
      }
    })
  }

  return {
    connected, running, mode, followDrafts, highlightNodeId, highlightStack,
    breakpoints, variables, logs, error, result, runs,
    visitedNodeIds, visitedTick,
    failedNodeIds, failedTick,
    skippedNodeIds, skippedTick,
    connect, disconnect, run, pause, step, stop, inspect, inspectNode,
    nodeResults, runInfo, fillRunInfo, setNodeResult, clearNodeResults,
    clearRuns, resetRunData, applyHistoryNodeStates,
    setFlowId, loadBreakpoints, breakpointIds,
    toggleBreakpoint, isBreakpoint, removeBreakpoints
  }
}
