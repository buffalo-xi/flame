import { ref } from 'vue'

// ── 运行级（对应 Flow_run_records）─────────────────────────────────────────────
// 一次运行的入参/限额/时间/状态等，调试 finished 帧 与 历史 GetRunDetail 共用。
export const runInfo = ref({
  id: null,
  flow_id: null,
  status: null,          // success / failed / cancelled
  run_type: null,
  result: null,
  err_msg: null,
  node_count: 0,
  duration_ms: 0,
  started_at: null,
  finished_at: null,
  run_args: null,        // 实际入参
  run_limits: null,      // 实际生效限额
  run_flow_vars: null,   // 流程变量最终值
  run_flow_vars_truncated: false,
  run_flow_vars_size: 0,
  progress: {},          // 流式消息 {progress: [...], result: [...]}
  progress_truncated: false,
  progress_size: 0,
})

// ── 节点级（对应 Flow_node_records）────────────────────────────────────────────
// 按 node_id 存各节点的执行快照，调试 node_snapshot/node_inspect 与 历史 GetRunDetail 的 nodes 共用。
export const nodeResults = ref({})

// 运行级填充（调试 finished / 历史 GetRunDetail 都调用）
export function fillRunInfo(run) {
  runInfo.value = run ? { ...run } : {
    id: null, flow_id: null, status: null, run_type: null, result: null, err_msg: null,
    node_count: 0, duration_ms: 0, started_at: null, finished_at: null,
    run_args: null, run_limits: null, run_flow_vars: null,
    run_flow_vars_truncated: false, run_flow_vars_size: 0,
    progress: {}, progress_truncated: false, progress_size: 0,
  }
}

// 节点级填充（按 node_id 覆盖，保留最后一次执行的数据，与后端去重语义一致）
export function setNodeResult(node) {
  if (node && node.node_id) {
    nodeResults.value[node.node_id] = node
  }
}

// 清空节点级数据（新一轮运行 / 切换查看对象时）
export function clearNodeResults() {
  nodeResults.value = {}
}
