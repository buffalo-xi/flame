// 运行上下文变量（builtin 变量源）的唯一真源。
//
// 与后端 flow.ExecContext.BuiltinVars() 手工同步：新增/删除键必须两边一起改，
// 否则选择器里选得到、运行时却报 unknown builtin variable（后端刻意不静默返回空值）。
//
// 选材标准：给程序用的，不给眼睛看的 —— 全部不可变、字符安全、能当键。
// 因此这里没有 user_name（可改、含特殊字符）、没有 req_id（只用于日志串联，
// 可能为空且不唯一）、没有 env（可能含密钥，走 env 变量源显式挑选）。
//
// 展示名与说明不在这里：按 key 放在 locales/{zh-CN,en}/flow.json 的 builtinVar 段。
export const BUILTIN_VARS = [
  { key: 'session_id', type: 'string' },
  { key: 'run_id', type: 'string' },
  { key: 'user_id', type: 'string' },
  { key: 'schema', type: 'string' },
  { key: 'flow_id', type: 'string' },
  // Output language of this run (BCP-47, e.g. zh-CN): a caller-pinned value or the calling agent's resolved language.
  // Empty string when nobody specified one - like session_id, it is a fact about the run, not a setting.
  { key: 'language', type: 'string' }
]
