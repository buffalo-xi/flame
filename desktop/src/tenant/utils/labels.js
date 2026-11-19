import i18n from '@/locales'

// 节点 label 全图唯一：source_ref / call.target / fork_node_id 都可以按 label 写，
// 重名会让这些引用指错节点。画布侧一律「撞名就加后缀」，不打断用户操作。

// 3 位字符/数字后缀：不用递增数字，几个节点并排时不会出现「代码-2 / 代码-3」互相追尾
export const randomLabelSuffix = () => {
  let s = ''
  while (s.length < 3) s += Math.random().toString(36).slice(2)
  return s.slice(0, 3)
}

/**
 * 返回不重名的 label（不冲突时原样返回）。
 * @param {string} base 想要的 label
 * @param {(label: string) => boolean} isTaken 该 label 是否已被别人占用
 */
export const uniqueLabel = (base, isTaken) => {
  const wanted = (base || '').trim() || i18n.global.t('flow.node.fallbackLabel.node')
  if (!isTaken(wanted)) return wanted
  for (let i = 0; i < 50; i++) {
    const candidate = `${wanted}-${randomLabelSuffix()}`
    if (!isTaken(candidate)) return candidate
  }
  return `${wanted}-${Date.now().toString(36).slice(-3)}`
}

export default { randomLabelSuffix, uniqueLabel }
