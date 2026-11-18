/**
 * 让自绘窗口（teleport + fixed 的面板 / 抽屉 / 浮层）也接受 ESC 关闭。
 *
 * Element Plus 的 el-dialog / el-drawer / ElMessageBox 自带 ESC 支持，
 * 但本项目大量窗口是自己画的（.settings-panel / .expose-panel / .ug-panel …），
 * 只有右上角一个 × —— 键盘用户只能去点鼠标。
 *
 * 用「栈」而不是每个组件各挂一份 document 监听：
 *   智能体设置面板 → 工具授权侧栏 → 变量选择弹窗
 * 三层同时开着时，一次 ESC 只该退一层（最上面那层），否则一按就全没了。
 *
 * 用法：
 *   useEscClose(() => props.visible, () => emit('close'))
 *   useEscClose(grantPickerVisible, () => { grantPickerVisible.value = false })
 *   useEscClose(visible, close, { skipWhenEditable: true })   // 面板内有内联重命名/行内编辑
 */

import { onBeforeUnmount, unref, watch } from 'vue'

// 当前打开着的窗口，按打开顺序入栈；只有栈顶响应 ESC
const stack = []
let bound = false

const isEditable = (el) => {
  if (!el || el.nodeType !== 1) return false
  return el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)
}

// 冒泡阶段监听：元素上的 @keydown.esc / el-select 自己的 ESC 处理先跑，
// 它们若 stopPropagation（el-select 会），这里就收不到，不会连带关掉面板。
const onKeydown = (e) => {
  if (e.key !== 'Escape' && e.key !== 'Esc') return
  // 中文输入法里 ESC 是用来取消候选的，别顺手把窗口关了
  if (e.isComposing) return
  const top = stack[stack.length - 1]
  if (!top) return
  if (!top.enabled()) return
  // 有 ElMessageBox 压在上面（「配置未保存，确定关闭？」）时，ESC 归它：
  // 否则它关掉后 catch 返回，我们这边又弹一次确认框，越按越多。
  if (document.querySelector('.el-message-box')) return
  // 正在输入框里改东西时，ESC 先归输入框（取消行内编辑），别把整个面板关掉
  if (top.skipWhenEditable && isEditable(e.target)) return
  top.close(e)
}

const bind = () => {
  if (bound) return
  document.addEventListener('keydown', onKeydown)
  bound = true
}

export function useEscClose(open, close, options = {}) {
  const { skipWhenEditable = false, enabled = () => true } = options
  const isOpen = () => (typeof open === 'function' ? open() : unref(open))
  const isEnabled = () => (typeof enabled === 'function' ? enabled() : unref(enabled))

  let token = null
  const push = () => {
    if (token) return
    token = { close, skipWhenEditable, enabled: isEnabled }
    stack.push(token)
    bind()
  }
  const pop = () => {
    if (!token) return
    const i = stack.indexOf(token)
    if (i >= 0) stack.splice(i, 1)
    token = null
  }

  watch(() => isOpen() === true, (v) => (v ? push() : pop()), { immediate: true })
  // 面板被父组件 v-if 摘掉时也要出栈，否则栈顶会留一个已经关掉的死窗口
  onBeforeUnmount(pop)
}
