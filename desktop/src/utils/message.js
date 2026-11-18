import { ElMessage } from 'element-plus'

// 全站统一的提示时长：成功 1s、错误 2s，警告 / 普通信息按 2s。
//
// 为什么在入口劫持而不是逐个调用点传 duration：全站 500+ 处提示、120+ 处还各自写了
// 700 / 1200 / 5000 之类的值，逐个改既容易漏，以后新写的又会忘。这里包一层，
// 调用点写法（ElMessage.success('已保存')）完全不变。
//
// 注意：这里强制覆盖 duration —— 调用点上再写 duration 不会生效，
// 否则「统一」无从谈起。确实需要更长的提示，请改这张表，而不是在调用点加参数。
const DURATION = {
  success: 1000,
  error: 2000,
  warning: 2000,
  info: 2000
}

// 带手动关闭按钮的提示也不特殊处理：统一时长是可预期的前提，例外越多越难记。
for (const type of Object.keys(DURATION)) {
  const original = ElMessage[type]
  if (typeof original !== 'function') continue

  ElMessage[type] = (options) => {
    const duration = DURATION[type]
    // 三种调用形态：'文本' / VNode / { message, offset, showClose, ... }
    if (options && typeof options === 'object' && !options.__v_isVNode) {
      return original.call(ElMessage, { ...options, duration })
    }
    return original.call(ElMessage, { message: options, duration })
  }
}

export default ElMessage
