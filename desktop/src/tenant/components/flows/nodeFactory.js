// 节点创建：顶层节点与容器内子节点共用一份模板。
//
// 两者原本是两份各 400 行的 switch，逐字重复；差异只有两处，都由 parent 参数表达：
//   1. 子节点多带 parentNode + extent（限制在父节点范围内），顶层不带；
//   2. zIndexGroup 子节点取父节点的、顶层取自身 id。
// 入口节点（entry）标签统一为「入口」：start / entry 的分工由 type 表达，
// 不再让 label 随容器类型（过程 / 循环 / 函数）或层级变化。
// 另外两个类型不对称：return 只能在容器内建（createNode 直接拒绝），
// function 只能在顶层建（createChildNode 直接返回 undefined）。
//
// 固定输出变量：节点创建时写入 config.outputs。
// 面板的保留名列表也由这里派生（FIXED_OUTPUTS = Object.keys(HTTP_OUTPUTS)），
// 避免「创建时写一份、面板里再列一份」两处漂移。

import { uniqueLabel } from '@/tenant/utils/labels.js'
import i18n from '@/locales'

const t = (key, params) => i18n.global.t(key, params)

export const HTTP_OUTPUTS = {
  mode: { type: 'string', get description() { return t('flow.nodeFactory.varMode') } },
  status: { type: 'number', get description() { return t('flow.nodeFactory.varStatus') } },
  headers: { type: 'string', get description() { return t('flow.nodeFactory.varHeaders') } },
  body: { type: 'string', get description() { return t('flow.nodeFactory.varBody') } },
  sse_events: { type: 'array[string]', get description() { return t('flow.nodeFactory.varSseEvents') } },
  stream_finished: { type: 'boolean', get description() { return t('flow.nodeFactory.varStreamFinished') } }
}

export const MCP_OUTPUTS = {
  result: { type: 'string', get description() { return t('flow.nodeFactory.varResult') } },
  progress: { type: 'array[string]', get description() { return t('flow.nodeFactory.varProgress') } },
  error: { type: 'string', get description() { return t('flow.nodeFactory.varError') } }
}

export const RPC_OUTPUTS = {
  result: { type: 'string', get description() { return t('flow.nodeFactory.varResult') } },
  progress: { type: 'array[string]', get description() { return t('flow.nodeFactory.varProgress') } },
  error: { type: 'string', get description() { return t('flow.nodeFactory.varError') } }
}

// 每个节点拿一份独立副本：面板后续会往 outputs 里增删变量
const cloneOutputs = (defs) => Object.fromEntries(
  Object.entries(defs).map(([key, def]) => [key, { ...def }])
)

// 依赖参数：节点数组、挂载与 id 生成都属于组件运行时状态，这里只接收不拥有。
export function createNodeBuilders({
  generateUniqueId, hasAncestorOfType, nodes, updateNodes, ElMessage
}) {
  // 节点 label 全图唯一：第二次拖入「代码」、第二个容器的「入口」都要自动改名
  const labelTaken = (label) => nodes.value.some(n => (n.data?.label || '').trim() === label)

  const build = (nodeType, x, y, parent, preset) => {
    let newNode
    let childNode
    switch (nodeType) {
      case 'branch':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.branch'),
            config: {
              children: [
                { id: generateUniqueId(), label: t('flow.node.branch.childName', { n: 1 }), condition: '', exclusive: true },
              ]
            }
          },
          position: { x, y },
          type: 'branch'
        }
        break
      case 'intent':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.intent'),
            config: {
              inputs: {
                messages: { source_type: 'argument', source_ref: '', source_key: '', type: '' },
                input: { source_type: 'argument', source_ref: '', source_key: '', type: '' }
              },
              llm: { gateway_model: '', system_prompt: t('flow.nodeFactory.intentPrompt'), params: {} },
              children: [
                { id: generateUniqueId(), label: t('flow.node.intent.childName', { n: 1 }), description: '' },
              ],
              outputs: { intent: { type: 'string' }, index: { type: 'number' } }
            }
          },
          position: { x, y },
          type: 'intent'
        }
        break
      case 'process':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.process'),
            // A visible starting number instead of an invisible "no limit": 8 leaves room for the ordinary fan-out and is
            // far below the tenant ceiling, and the author can clear it or raise it in the properties panel.
            config: { parallel: { max_concurrency: 8 } }
          },
          position: { x, y },
          type: 'process'
        }
        childNode = {
          id: generateUniqueId(),
          parentNode: newNode.id,
          data: {
            label: t('flow.node.defaultLabel.entry'),
          },
          position: { x: 6, y: 37 },
          extent: {
            range: 'parent',
            padding: [37, 5, 5, 5],
          },
          type: 'entry'
        }
        break
      case 'call':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.call'),
          },
          position: { x, y },
          type: 'call'
        }
        break
      case 'memory':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.memory'),
            config: {
              outputs: { messages: { type: 'array[object]' } },
              limit: 10,
              window: { enabled: false, unit: 'hour', value: 24 }
            }
          },
          position: { x, y },
          type: 'memory'
        }
        break
      case 'assign':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.assign'),
            config: {
              inputs: {},
              outputs: {},
              targets: {},
              assignments: []
            }
          },
          position: { x, y },
          type: 'assign'
        }
        break
      case 'raise':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.raise'),
            config: {
              inputs: {},
              raise: { code: 'other', message: '' }
            }
          },
          position: { x, y },
          type: 'raise'
        }
        break
      case 'loop':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.loop'),
            config: {
              loop: { type: 'count', total_times: 10 },
              loop_vars: { index: { type: 'number', get description() { return t('flow.nodeFactory.loopVarIndex') } } }
            }
          },
          position: { x, y },
          type: 'loop'
        }
        childNode = {
          id: generateUniqueId(),
          parentNode: newNode.id,
          data: {
            label: t('flow.node.defaultLabel.entry'),
          },
          position: { x: 6, y: 37 },
          extent: {
            range: 'parent',
            padding: [37, 5, 5, 5],
          },
          type: 'entry'
        }
        break
      case 'break':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.breakOut'),
          },
          position: { x, y },
          type: 'break'
        }
        break
      case 'continue':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.continue'),
          },
          position: { x, y },
          type: 'continue'
        }
        break
      case 'return':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.return'),
          },
          position: { x, y },
          type: 'return'
        }
        break
      case 'exit':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.exit'),
          },
          position: { x, y },
          type: 'exit'
        }
        break
      case 'http':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.http'),
            config: {
              outputs: cloneOutputs(HTTP_OUTPUTS)
            }
          },
          position: { x, y },
          type: 'http'
        }
        break
      case 'mcp':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: 'MCP',
            config: {
              outputs: cloneOutputs(MCP_OUTPUTS)
            }
          },
          position: { x, y },
          type: 'mcp'
        }
        break
      case 'rpc':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: 'RPC',
            config: {
              outputs: cloneOutputs(RPC_OUTPUTS)
            }
          },
          position: { x, y },
          type: 'rpc'
        }
        break
      case 'code':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.code'),
          },
          position: { x, y },
          type: 'code'
        }
        break
      case 'layout':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.fallbackLabel.layout'),
          },
          position: { x, y },
          type: 'layout'
        }
        break
      case 'comment':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.comment'),
          },
          position: { x, y },
          type: 'comment'
        }
        break
      case 'result':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.result'),
          },
          position: { x, y },
          type: 'result'
        }
        break
      case 'progress':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.progress'),
          },
          position: { x, y },
          type: 'progress'
        }
        break
      case 'llm':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: 'LLM',
          },
          position: { x, y },
          type: 'llm'
        }
        break   
      case 'function':
        newNode = {
          id: generateUniqueId(),
          data: {
            label: t('flow.node.defaultLabel.function'),
          },
          position: { x, y },
          type: 'function'
        }
        childNode = {
          id: generateUniqueId(),
          parentNode: newNode.id,
          data: {
            label: t('flow.node.defaultLabel.entry'),
          },
          position: { x: 6, y: 37 },
          extent: {
            range: 'parent',
            padding: [37, 5, 5, 5], // 距离父节点四边各20px
          },
          type: 'entry'
        }
        break
      default:
        return
    }
    if (!newNode) return
    // 工具目录里点进来的条目：把连接 / 端点预填进 config，作者只需补参数
    if (preset) newNode.data.config = { ...(newNode.data.config || {}), ...preset }
    // label 要唯一：模板里的默认名（代码 / 入口 / 结果输出…）撞名时自动加 3 位后缀。
    // 注意 config.children 里的「分支 1 / 意图 1」不是节点 label，不参与唯一性。
    newNode.data.label = uniqueLabel(newNode.data.label, labelTaken)
    if (childNode) childNode.data.label = uniqueLabel(childNode.data.label, labelTaken)
    if (parent) {
      newNode.parentNode = parent
      newNode.extent = { range: 'parent', padding: [37, 5, 5, 5] }
      const parentNodeObj = nodes.value.find(n => n.id === parent)
      newNode.zIndexGroup = parentNodeObj?.zIndexGroup
    } else {
      newNode.zIndexGroup = newNode.id
    }
    const newNodes = [...nodes.value, newNode]
    if (childNode) {
      childNode.zIndexGroup = newNode.zIndexGroup
      newNodes.push(childNode)
    }
    updateNodes(newNodes)
    return newNode
  }

  // 顶层创建：return 只能在函数节点内部用
  const createNode = (nodeType, x, y, preset) => {
    if (nodeType === 'return') {
      ElMessage.warning(t('flow.nodeFactory.returnOnlyInFunction'))
      return null
    }
    return build(nodeType, x, y, null, preset)
  }

  // 容器内创建子节点：函数节点只能建在顶层
  const createChildNode = (parentNode, nodeType, x, y, preset) => {
    if (nodeType === 'return' && !hasAncestorOfType(parentNode, 'function')) {
      ElMessage.warning(t('flow.nodeFactory.returnOnlyInFunction'))
      return null
    }
    if (nodeType === 'function') return
    return build(nodeType, x, y, parentNode, preset)
  }

  return { createChildNode, createNode }
}
