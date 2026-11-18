// 节点并发配置的唯一解析/构造实现（Code / Http / Call / Loop / Process 五个节点共用）。
//
// 落库形状：
//   缺省                          = 串行
//   { mode: 'mono' }              = 独立并发（不属于任何 fork 组）
//   { mode: 'fork', fork_node_id } = 加入指定 fork 组
//
// 节点上的下拉只有一个值：'none' | 'mono' | <fork 节点 id>，
// 与落库形状的互转全部收敛在这里，避免五个组件各写一份再次漂移。

export const CONCURRENCY_NONE = 'none'

// readConcurrencyGroup: config → 下拉选中的值
export const readConcurrencyGroup = (config) => {
  const c = config?.concurrency
  if (!c || typeof c !== 'object') return CONCURRENCY_NONE
  if (c.mode === 'fork') return c.fork_node_id || CONCURRENCY_NONE
  return c.mode === 'mono' ? 'mono' : CONCURRENCY_NONE
}

// buildConcurrencyConfig: 下拉选中的值 → config.concurrency
export const buildConcurrencyConfig = (group) => {
  if (!group || group === CONCURRENCY_NONE) return undefined
  if (group === 'mono') return { mode: 'mono' }
  return { mode: 'fork', fork_node_id: group }
}
