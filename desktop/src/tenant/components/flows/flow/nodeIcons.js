// 节点图标的唯一实现：节点类型 → 图标组件 / 节点类型 → CSS 类。
//
// 背景：这套表原先在 12 个面板里各有一份副本，已经漂移 —— LLM 面板里
// code / result / progress / terminate 全指向 Help（变量来源里这些节点显示通用问号），
// 各面板普遍留着已废弃的 terminate / agent / subflow，又都缺 mcp / rpc / memory / intent。
// 新增一种节点类型只需要改本文件，漏改不会再让某个面板的图标悄悄变问号。
//
// class 只负责配色，复用各面板 <style> 里已有的 .icon-* 规则；没有独立规则的类型
// 借一个色相最近的类（见行尾注释），否则「类名没有对应颜色」会让图标变成透明方块。
import {
  Aim, ChatLineSquare, CircleCheck, Clock, Coin, Collection, Connection,
  Grid, Help, Lightning, MagicStick, Minus, Monitor, Mouse, Promotion, RefreshLeft,
  RefreshRight, ScaleToOriginal, SwitchButton, Timer, Tools, Upload, WarnTriangleFilled
} from '@element-plus/icons-vue'

const COMPONENTS = {
  default: Help,
  start: Timer,
  entry: Aim,               // 与 EntryNode 的入口徽标一致
  layout: Grid,             // 占位/分组容器
  comment: ChatLineSquare,  // 纯视觉注释
  branch: Connection,
  intent: Aim,
  process: Help,
  function: ScaleToOriginal,
  call: Mouse,
  assign: Coin,
  raise: WarnTriangleFilled,   // failure, not control flow: a warning sign
  loop: RefreshLeft,
  memory: Clock,
  break: Minus,
  continue: Upload,
  return: CircleCheck,
  exit: SwitchButton,
  code: MagicStick,
  http: Lightning,
  mcp: Tools,
  rpc: Mouse,
  result: Promotion,
  progress: RefreshRight,
  llm: ChatLineSquare,
  knowledge: Collection     // 存量类型：后端没有执行器，仅为渲染老流程图
}

const CLASSES = {
  default: 'icon-default',
  start: 'icon-start',
  entry: 'icon-start',        // 入口与 start 同色 #00704A
  layout: 'icon-default',
  comment: 'icon-default',
  branch: 'icon-branch',
  intent: 'icon-return',      // 无 icon-intent：return 同为紫色 #9c27b0
  process: 'icon-process',
  function: 'icon-function',
  call: 'icon-call',
  assign: 'icon-assign',
  raise: 'icon-raise',        // failure red #f56c6c: lighter than exit (#c62828), which is a deliberate stop
  loop: 'icon-loop',
  memory: 'icon-loop',        // 无 icon-memory：借同为紫色的 loop
  break: 'icon-break',
  continue: 'icon-continue',
  return: 'icon-return',
  exit: 'icon-exit',          // 电源符号 + 深红 #c62828，与 code 的魔棒 / 赭石分开
  code: 'icon-code',
  http: 'icon-http',
  mcp: 'icon-progress',       // 无 icon-mcp：progress #eea236 ≈ mcp #e6a23c
  rpc: 'icon-http',           // 无 icon-rpc：http #409eff ≈ rpc #1890ff
  result: 'icon-result',
  progress: 'icon-progress',
  llm: 'icon-llm',
  knowledge: 'icon-knowledge'
}

export const getNodeIconComponent = (type) => COMPONENTS[type] || COMPONENTS.default

export const getNodeIconClass = (type) => CLASSES[type] || CLASSES.default
