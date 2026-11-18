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

export const HTTP_OUTPUTS = {
  mode: { type: 'string', description: '响应模式' },
  status: { type: 'number', description: 'HTTP 状态码' },
  headers: { type: 'string', description: '响应头' },
  body: { type: 'string', description: '响应体' },
  sse_events: { type: 'array[string]', description: 'SSE 事件列表' },
  stream_finished: { type: 'boolean', description: '流式传输是否完成' }
}

export const MCP_OUTPUTS = {
  result: { type: 'string', description: '工具返回结果' },
  progress: { type: 'array[string]', description: '进度通知数组' },
  error: { type: 'string', description: '调用错误信息' }
}

export const RPC_OUTPUTS = {
  result: { type: 'string', description: '工具返回结果' },
  progress: { type: 'array[string]', description: '进度通知数组' },
  error: { type: 'string', description: '调用错误信息' }
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

  const build = (nodeType, x, y, parent) => {
    let newNode
    let childNode
    switch (nodeType) {
      case 'branch':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '条件分支',
            config: {
              children: [
                { id: generateUniqueId(5), label: '分支 1', condition: '', exclusive: true },
              ]
            }
          },
          position: { x, y },
          type: 'branch'
        }
        break
      case 'intent':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '意图',
            config: {
              inputs: {
                messages: { source_type: 'argument', source_ref: '', source_key: '', type: '' },
                input: { source_type: 'argument', source_ref: '', source_key: '', type: '' }
              },
              llm: { gateway_model: '', system_prompt: '你是意图分类器，请根据对话历史和用户输入判断用户意图。\n\n对话历史：\n{{json .input.messages}}\n\n用户输入：\n{{.input.input}}', params: {} },
              children: [
                { id: generateUniqueId(5), label: '意图 1', description: '' },
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
          id: generateUniqueId(5),
          data: {
            label: '嵌入流程',
          },
          position: { x, y },
          type: 'process'
        }
        childNode = {
          id: generateUniqueId(5),
          parentNode: newNode.id,
          data: {
            label: '入口',
          },
          position: { x: 6, y: 37 },
          extent: {
            range: 'parent',
            padding: [37, 5, 5, 5],
          },
          type: 'entry'
        }
        break
      case 'fork':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '并发组',
          },
          position: { x, y },
          type: 'fork'
        }
        break
      case 'wait':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '并发同步',
          },
          position: { x, y },
          type: 'wait'
        }
        break
      case 'call':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '调用',
          },
          position: { x, y },
          type: 'call'
        }
        break
      case 'memory':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '记忆',
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
          id: generateUniqueId(5),
          data: {
            label: '赋值',
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
      case 'loop':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '循环',
            config: {
              loop: { type: 'count', total_times: 10 },
              loop_vars: { index: { type: 'number', description: '循环下标' } }
            }
          },
          position: { x, y },
          type: 'loop'
        }
        childNode = {
          id: generateUniqueId(5),
          parentNode: newNode.id,
          data: {
            label: '入口',
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
          id: generateUniqueId(5),
          data: {
            label: '跳出',
          },
          position: { x, y },
          type: 'break'
        }
        break
      case 'continue':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '跳过',
          },
          position: { x, y },
          type: 'continue'
        }
        break
      case 'return':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '返回',
          },
          position: { x, y },
          type: 'return'
        }
        break
      case 'exit':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '终止',
          },
          position: { x, y },
          type: 'exit'
        }
        break
      case 'http':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: 'HTTP 请求',
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
          id: generateUniqueId(5),
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
          id: generateUniqueId(5),
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
          id: generateUniqueId(5),
          data: {
            label: '代码',
          },
          position: { x, y },
          type: 'code'
        }
        break
      case 'layout':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '布局',
          },
          position: { x, y },
          type: 'layout'
        }
        break
      case 'comment':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '注释',
          },
          position: { x, y },
          type: 'comment'
        }
        break
      case 'result':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '结果输出',
          },
          position: { x, y },
          type: 'result'
        }
        break
      case 'progress':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '进度输出',
          },
          position: { x, y },
          type: 'progress'
        }
        break
      case 'llm':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: 'LLM',
          },
          position: { x, y },
          type: 'llm'
        }
        break   
      case 'function':
        newNode = {
          id: generateUniqueId(5),
          data: {
            label: '函数',
          },
          position: { x, y },
          type: 'function'
        }
        childNode = {
          id: generateUniqueId(5),
          parentNode: newNode.id,
          data: {
            label: '入口',
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
  const createNode = (nodeType, x, y) => {
    if (nodeType === 'return') {
      ElMessage.warning('返回节点只能创建在函数节点内部')
      return null
    }
    return build(nodeType, x, y, null)
  }

  // 容器内创建子节点：函数节点只能建在顶层
  const createChildNode = (parentNode, nodeType, x, y) => {
    if (nodeType === 'return' && !hasAncestorOfType(parentNode, 'function')) {
      ElMessage.warning('返回节点只能创建在函数节点内部')
      return null
    }
    if (nodeType === 'function') return
    return build(nodeType, x, y, parentNode)
  }

  return { createChildNode, createNode }
}
