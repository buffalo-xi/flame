/**
 * 工作流页（Flows.vue 及其子组件）共用的纯格式化 / 元数据。
 * 抽出来的原因：卡片、编辑页头部、端点面板三处都要用，留在某个组件里只能靠重复定义或层层传参。
 */

// 工作流两态（与后端 shared/models 对应；agent / skill 仍是三态，各自维护映射）：
//   1 正常：可修改、可运行（新建默认）—— 叫「正常」而不是「编辑」：它是状态名，
//     不是动作；列表上写「编辑」会让人以为是个按钮
//   3 废弃：不可修改、不可运行（只由手工设置）
export const STATUS_META = {
  1: { text: '正常', tag: 'success' },
  3: { text: '废弃', tag: 'danger' }
}

export const statusMeta = (status) => STATUS_META[status] || STATUS_META[1]

// 卡片名样式：废弃=红（编辑态不加类）
export const titleClass = (status) => (status === 3 ? 'title-disabled' : '')

// 名称规则：字母或数字开头，后续只允许字母数字横线，长度 ≤64。
// agent / skill：名字是磁盘目录名，路径安全逼出来的。
// flow：名字会变成端点名 → MCP 的 tool.name 与 agent 侧 local__<name> 与授权 ref
//       local:rpc:<name>，这几处只认 ASCII，所以规则相同但理由不同（中文写描述）。
export const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9-]*$/

// 协议颜色映射
export const getProtocolColor = (protocol) => {
  const map = { mcp: '#52c41a', rpc: '#1890ff', llm: '#fa8c16' }
  return map[protocol] || '#909399'
}

// 端点文字颜色：对外开放按协议配色，仅内部灰色
export const getEndpointTextColor = (p) => {
  if (!p.exposed) return '#909399'
  return getProtocolColor(p.protocol)
}

// 格式化时间（年月日时分秒）
export const formatTime = (time) => {
  if (!time) return '-'
  const date = new Date(time)
  if (isNaN(date.getTime())) return '-'
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const hours = String(date.getHours()).padStart(2, '0')
  const minutes = String(date.getMinutes()).padStart(2, '0')
  const seconds = String(date.getSeconds()).padStart(2, '0')
  return `${year}/${month}/${day} ${hours}:${minutes}:${seconds}`
}

// 只到日期的格式化（年月日）
export const formatDate = (time) => {
  if (!time) return '-'
  const date = new Date(time)
  if (isNaN(date.getTime())) return '-'
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}/${month}/${day}`
}
