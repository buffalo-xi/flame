import { h } from 'vue'
import StartNode from './flow/StartNode.vue'
import BranchNode from './flow/BranchNode.vue'
import IntentNode from './flow/IntentNode.vue'
import ProcessNode from './flow/ProcessNode.vue'
import EntryNode from './flow/EntryNode.vue'
import CodeNode from './flow/CodeNode.vue'
import HttpNode from './flow/HttpNode.vue'
import McpNode from './flow/McpNode.vue'
import RpcNode from './flow/RpcNode.vue'
import LayoutNode from './flow/LayoutNode.vue'
import ResultNode from './flow/ResultNode.vue'
import ProgressNode from './flow/ProgressNode.vue'
import LLMNode from './flow/LLMNode.vue'
import KnowledgeNode from './flow/KnowledgeNode.vue'
import BreakNode from './flow/BreakNode.vue'
import ContinueNode from './flow/ContinueNode.vue'
import ReturnNode from './flow/ReturnNode.vue'
import ExitNode from './flow/ExitNode.vue'
import FunctionNode from './flow/FunctionNode.vue'
import CallNode from './flow/CallNode.vue'
import AssignNode from './flow/AssignNode.vue'
import RaiseNode from './flow/RaiseNode.vue'
import LoopNode from './flow/LoopNode.vue'
import MemoryNode from './flow/MemoryNode.vue'
import CommentNode from './flow/CommentNode.vue'

// 节点类型映射（Vue Flow 的 :node-types）。
// 26 个节点组件只在这里被引用，所以整块连同 import 一起搬出 Flow.vue。
// 依赖以参数注入：这些操作属于组件运行时状态，.js 不去反向引用它。
export function createNodeTypes({
  deleteNode, getNodeById, handleNodeHandleClick, handleUpdateNodeData, onBranchDeleteChild, updateEdges, updateNodeLabel
}) {
  return {
  start: (props) => h(StartNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onUpdate:edges': (nodeId, newHandle) => updateEdges(nodeId, newHandle),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    getNodeById: getNodeById
  }),
  branch: (props) => h(BranchNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDeleteChild': (childId) => onBranchDeleteChild(childId),
    getNodeById: getNodeById
  }),
  intent: (props) => h(IntentNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDeleteChild': (childId) => onBranchDeleteChild(childId),
    getNodeById: getNodeById
  }),
  process: (props) => h(ProcessNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onUpdateData': (updateData) => handleUpdateNodeData(updateData),
    getNodeById: getNodeById
  }),
  function: (props) => h(FunctionNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    getNodeById: getNodeById
  }),
  entry: (props) => h(EntryNode, {
    ...props,
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    getNodeById: getNodeById
  }),
  call: (props) => h(CallNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onUpdateData': (updateData) => handleUpdateNodeData(updateData),
    getNodeById: getNodeById
  }),
  assign: (props) => h(AssignNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  // No onUpdate:edges: raise is a sink, so it never contributes a new outgoing handle.
  raise: (props) => h(RaiseNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  loop: (props) => h(LoopNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onUpdateData': (updateData) => handleUpdateNodeData(updateData),
    getNodeById: getNodeById
  }),
  memory: (props) => h(MemoryNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  comment: (props) => h(CommentNode, {
    ...props,
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onUpdateData': (updateData) => handleUpdateNodeData(updateData),
    getNodeById: getNodeById
  }),
  break: (props) => h(BreakNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  continue: (props) => h(ContinueNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  return: (props) => h(ReturnNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  exit: (props) => h(ExitNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  code: (props) => h(CodeNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onUpdate:edges': (nodeId, newHandle) => updateEdges(nodeId, newHandle),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onUpdateData': (updateData) => handleUpdateNodeData(updateData),
    getNodeById: getNodeById
  }),
  http: (props) => h(HttpNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onUpdate:edges': (nodeId, newHandle) => updateEdges(nodeId, newHandle),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    'onUpdateData': (updateData) => handleUpdateNodeData(updateData),
    getNodeById: getNodeById
  }),
  layout: (props) => h(LayoutNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  result: (props) => h(ResultNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  progress: (props) => h(ProgressNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  llm: (props) => h(LLMNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  mcp: (props) => h(McpNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  rpc: (props) => h(RpcNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  // 历史类型：后端没有 knowledge 执行器，palette 也已无入口，保留映射只为渲染存量流程图
  knowledge: (props) => h(KnowledgeNode, {
    ...props,
    'onUpdate:label': (newLabel) => updateNodeLabel(props.id, newLabel),
    'onHandleClick': (nodeId, handleId, event) => {
      handleNodeHandleClick(nodeId, handleId, event)
    },
    'onDelete': (nodeId) => deleteNode(nodeId),
    getNodeById: getNodeById
  }),
  }
}
