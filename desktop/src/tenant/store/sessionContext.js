import { reactive } from 'vue'

// 当前会话上下文：跨组件共享的「正在进行的会话」。
// 为什么需要它：Chat.vue 是会话状态的唯一来源，但 Chat 与 Workbench 是兄弟组件，
// Vue 的 provide/inject 只能向下传递，Workbench 拿不到 Chat 内部的 selectedAgentId /
// currentSessionId。这里用一个模块级 reactive 做最轻量的共享：Chat 写入，
// Workbench 的「工作区」面板读取（读会话产生的文件）。
export const sessionContext = reactive({
  agentId: '',
  sessionId: '',
  // 「请工作台打开这个文件」的信号。用 nonce 而不是直接传路径：
  // 同一个路径连点两次也要能重新打开（只 watch path 的话第二次不触发）。
  openPath: '',
  openNonce: 0,
})

// 请求工作台打开工作区里的某个文件（相对路径，如 uploads/a.md）。
// 目录路径以 / 结尾，用于「跳到某个目录」。
export function requestOpenWsFile(path) {
  const p = String(path || '').trim()
  if (!p) return
  sessionContext.openPath = p
  sessionContext.openNonce++
}
