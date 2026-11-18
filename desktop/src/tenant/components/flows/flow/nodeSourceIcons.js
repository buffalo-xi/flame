// 变量来源图标的唯一实现。
//
// 背景：这段逻辑原先在 12 个 *NodeProps.vue 里各有一份副本（Code / Http / Rpc / Mcp /
// LLM / Loop / Call / Branch / Assign / Intent / Result / Progress），且已经开始漂移 ——
// 有的返回 isNode 标记有的没有，节点图标表也各不相同。新增一种来源（builtin）得改 12 处，
// 漏掉一处不会有编译错误，只会「图标不出现」，只能靠肉眼发现。
//
// 现在统一到这里：来源类型 → 图标的映射只在本文件维护；节点类型的图标直接取 nodeIcons.js，
// 调用方不用再各自传解析器。
import { ChromeFilled, Notification, User, Tools, Key } from '@element-plus/icons-vue'
import { getNodeIconComponent, getNodeIconClass } from './nodeIcons.js'

// resolveSourceIcon 解析一个变量来源的展示图标。
//   sourceType / sourceRef —— 变量定义里的 source_type / source_ref
//   nodes —— 当前流程图节点数组，用于把 source_ref 解析成节点名
// 返回 null 表示无可显示图标，模板会退化成只显示变量名。
export const resolveSourceIcon = (sourceType, sourceRef, nodes) => {
  if (sourceType === 'flow') {
    return { component: ChromeFilled, class: 'icon-source-flow', isNode: false }
  }
  if (sourceType === 'env') {
    return { component: Notification, class: 'icon-source-env', isNode: false }
  }
  // 全局变量：与环境变量同为「租户级外部输入」，所以借用 env 的图标样式 ——
  // 只为一种来源去 12 个面板各加一条 scoped 规则，代价大于收益。
  if (sourceType === 'global') {
    return { component: Key, class: 'icon-source-env', isNode: false }
  }
  if (sourceType === 'argument') {
    return { component: User, class: 'icon-source-argument', isNode: false }
  }
  // 运行上下文（builtin）：不指向任何节点，所以没有 nodeLabel
  if (sourceType === 'builtin') {
    return { component: Tools, class: 'icon-source-builtin', isNode: false }
  }
  if (sourceType === 'node' && sourceRef) {
    const node = (nodes || []).find(n => String(n.id) === String(sourceRef))
    if (node) {
      const type = node.type
      return {
        component: getNodeIconComponent(type),
        class: getNodeIconClass(type),
        nodeLabel: node.data?.label || node.id,
        isNode: true
      }
    }
  }
  return null
}
