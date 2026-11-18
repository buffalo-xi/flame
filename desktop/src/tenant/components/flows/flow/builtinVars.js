// 运行上下文变量（builtin 变量源）的唯一真源。
//
// 与后端 flow.ExecContext.BuiltinVars() 手工同步：新增/删除键必须两边一起改，
// 否则选择器里选得到、运行时却报 unknown builtin variable（后端刻意不静默返回空值）。
//
// 选材标准：给程序用的，不给眼睛看的 —— 全部不可变、字符安全、能当键。
// 因此这里没有 user_name（可改、含特殊字符）、没有 req_id（只用于日志串联，
// 可能为空且不唯一）、没有 env（可能含密钥，走 env 变量源显式挑选）。
export const BUILTIN_VARS = [
  { key: 'session_id', type: 'string', label: '会话 ID', hint: '无会话调用时为空串' },
  { key: 'run_id', type: 'string', label: '运行 ID', hint: '每次运行唯一，可用作幂等键' },
  { key: 'user_id', type: 'string', label: '调用用户 ID', hint: '未认证调用时为空串' },
  { key: 'schema', type: 'string', label: '租户 Schema', hint: '多租户外部 key 前缀' },
  { key: 'flow_id', type: 'string', label: '流程 ID', hint: '当前流程实例' }
]
