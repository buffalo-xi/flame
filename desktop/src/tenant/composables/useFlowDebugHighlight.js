import { watch } from 'vue'

// 调试态高亮：把「当前执行 / 断点 / 已运行 / 出错 / 跳过」这几类标记同步到节点 class 上。
// 全部走 Vue Flow 的 updateNode 精准改单节点 class，不重建 nodes 数组 —— 否则每来一次
// 调试事件整张图都会重渲染。
//
// 为什么需要独立的 debugClasses Map：updateNode 改的是 Vue Flow 内部 store，不会同步回
// 本地 nodes.value，从 nodes.value 读当前 class 会拿到初始值，add/remove 互相覆盖。
//
// 与 useFlowDebug.js 的分工：那边管 WS 通道与运行数据，这里只管「数据怎么画到节点上」。
export function useFlowDebugHighlight({ flowDebug, nodes, updateNode }) {
  // 当前执行节点高亮：用 updateNode 精准改单节点 class，不重建 nodes 数组（避免频繁刷新）
  // 运行态（mode='run'）用 debug-running 旋转图标，断点挂起态（mode='pause'）用 debug-active pause 图标。
  let lastHiNode = ''
  watch(flowDebug.highlightNodeId, (id) => {
    if (lastHiNode && lastHiNode !== id) {
      updateNode(lastHiNode, { class: removeDebugClass(lastHiNode, 'debug-active') })
      updateNode(lastHiNode, { class: removeDebugClass(lastHiNode, 'debug-running') })
    }
    if (id) {
      if (flowDebug.mode.value === 'pause') {
        updateNode(id, { class: addDebugClass(id, 'debug-active') })
        updateNode(id, { class: removeDebugClass(id, 'debug-running') })
      } else {
        updateNode(id, { class: addDebugClass(id, 'debug-running') })
        updateNode(id, { class: removeDebugClass(id, 'debug-active') })
      }
    }
    lastHiNode = id
  })
  // mode 切换（run<->pause）时，就地切换当前节点的图标形态
  watch(flowDebug.mode, (m) => {
    const id = flowDebug.highlightNodeId.value
    if (!id) return
    if (m === 'pause') {
      updateNode(id, { class: addDebugClass(id, 'debug-active') })
      updateNode(id, { class: removeDebugClass(id, 'debug-running') })
    } else {
      updateNode(id, { class: addDebugClass(id, 'debug-running') })
      updateNode(id, { class: removeDebugClass(id, 'debug-active') })
    }
  })

  // class 字符串增删（仅针对调试相关 class，不动其它类）
  // 注意：updateNode 更新的是 Vue Flow 内部 store，不会同步回本地 nodes.value，
  // 因此不能从 nodes.value 读当前 class（会拿到初始值导致互相覆盖）。
  // 这里用一个独立的 debugClasses Map 维护每个节点当前已加的调试 class，
  // add/remove 都基于它计算，再拼上节点的原始 class 传给 updateNode。
  const debugClasses = new Map() // nodeId -> Set<调试class>
  function baseClassOf(nodeId) {
    const n = nodes.value.find((x) => x.id === nodeId)
    return (n && n.class) ? String(n.class).split(/\s+/).filter((c) => c && !isDebugClass(c)) : []
  }
  function isDebugClass(c) {
    return c === 'debug-active' || c === 'debug-visited' || c === 'debug-error' || c === 'debug-skipped' || c === 'has-breakpoint' || c === 'debug-running'
  }
  function getClassOf(nodeId) {
    const base = baseClassOf(nodeId)
    const debug = debugClasses.get(nodeId) || new Set()
    return [...base, ...debug].join(' ')
  }
  function addDebugClass(nodeId, cls) {
    let s = debugClasses.get(nodeId)
    if (!s) { s = new Set(); debugClasses.set(nodeId, s) }
    s.add(cls)
    return getClassOf(nodeId)
  }
  function removeDebugClass(nodeId, cls) {
    const s = debugClasses.get(nodeId)
    if (s) s.delete(cls)
    return getClassOf(nodeId)
  }

  // 断点红点：watch breakpoints 集合，用 updateNode 精准改 has-breakpoint class（不重建 nodes 数组）
  watch(
    () => [...flowDebug.breakpoints],
    (ids) => {
      const set = new Set(ids)
      nodes.value.forEach((n) => {
        const has = (getClassOf(n.id).split(/\s+/).includes('has-breakpoint'))
        if (set.has(n.id) && !has) {
          updateNode(n.id, { class: addDebugClass(n.id, 'has-breakpoint') })
        } else if (!set.has(n.id) && has) {
          updateNode(n.id, { class: removeDebugClass(n.id, 'has-breakpoint') })
        }
      })
    },
    { deep: true, immediate: true }
  )

  // 已运行节点高亮：watch visitedNodeIds（由 visitedTick 触发），增量处理——
  // 只对新出现的节点加 debug-visited，清空时统一移除，全程 updateNode 不重建 nodes 数组。
  let visitedMarked = new Set()
  watch(
    () => flowDebug.visitedTick.value,
    () => {
      const current = new Set(flowDebug.visitedNodeIds.value)
      // diff：移除不在 current 的旧标记
      visitedMarked.forEach((id) => {
        if (!current.has(id)) {
          updateNode(id, { class: removeDebugClass(id, 'debug-visited') })
          visitedMarked.delete(id)
        }
      })
      // 新增：给新出现的节点加标记
      current.forEach((id) => {
        if (!visitedMarked.has(id)) {
          updateNode(id, { class: addDebugClass(id, 'debug-visited') })
          visitedMarked.add(id)
        }
      })
    }
  )

  // 出错节点标记：watch failedNodeIds（由 failedTick 触发），增量处理，
  // 与 debug-visited 同思路，用 updateNode 精准改单节点 class 不重建 nodes。
  let failedMarked = new Set()
  watch(
    () => flowDebug.failedTick.value,
    () => {
      const current = new Set(flowDebug.failedNodeIds.value)
      // diff：移除不在 current 的旧标记
      failedMarked.forEach((id) => {
        if (!current.has(id)) {
          updateNode(id, { class: removeDebugClass(id, 'debug-error') })
          failedMarked.delete(id)
        }
      })
      // 新增：给新出现的节点加标记
      current.forEach((id) => {
        if (!failedMarked.has(id)) {
          updateNode(id, { class: addDebugClass(id, 'debug-error') })
          failedMarked.add(id)
        }
      })
    }
  )

  // 跳过节点标记：watch skippedNodeIds（由 skippedTick 触发），增量处理，同出错节点。
  let skippedMarked = new Set()
  watch(
    () => flowDebug.skippedTick.value,
    () => {
      const current = new Set(flowDebug.skippedNodeIds.value)
      // diff：移除不在 current 的旧标记
      skippedMarked.forEach((id) => {
        if (!current.has(id)) {
          updateNode(id, { class: removeDebugClass(id, 'debug-skipped') })
          skippedMarked.delete(id)
        }
      })
      // 新增：给新出现的节点加标记
      current.forEach((id) => {
        if (!skippedMarked.has(id)) {
          updateNode(id, { class: addDebugClass(id, 'debug-skipped') })
          skippedMarked.add(id)
        }
      })
    }
  )
}
