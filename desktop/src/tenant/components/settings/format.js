import { ElMessage } from 'element-plus'

/** 脱敏显示密钥：保留首尾各 4 位 */
export const maskSecret = (val, head = 4, tail = 4) => {
  if (!val) return ''
  if (val.length <= head + tail) return '****'
  return val.slice(0, head) + '****' + val.slice(-tail)
}

/** 脱敏显示环境变量（KEY=VALUE → KEY=***，只隐藏值） */
export const maskEnv = (env) => {
  if (!env || typeof env !== 'string') return ''
  const idx = env.indexOf('=')
  if (idx <= 0) return env // 无等号或非 KEY=VALUE 形式原样展示
  const key = env.slice(0, idx)
  return `${key}=***`
}

/** 复制到剪贴板（失败给出提示，不抛错） */
export const copyText = async (text) => {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制到剪贴板')
  } catch (e) {
    ElMessage.error('复制失败，请手动复制')
  }
}
