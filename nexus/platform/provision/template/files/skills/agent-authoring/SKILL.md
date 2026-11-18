# Agent 编写指南

## 用途

指导你创建 / 修改 / 删除 agent（智能体），并写出高质量的 `AGENT.md`。

## 何时使用

出现这些意图时读本 skill：

- 「创建一个（专门做 X 的）智能体 / agent」
- 「建个写诗 / 写笑话 / 聊天 的机器人」
- 「给我配一个负责 Y 的助手」
- 「改一下某个 agent 的名字 / 描述」「删掉某个 agent」「现在有哪些 agent？」

**只做一次、或用户没要求做成可复用 agent 的任务，不要建 agent。**

## 怎么做

### 1. 先确认三件事

| 项 | 要求 |
|---|---|
| `name` | 唯一，只含**字母、数字、横线**（`^[a-zA-Z0-9][a-zA-Z0-9-]*$`），不能有下划线、不能以横线开头，≤64 字符。小写+横线，如 `poet`、`english-mate`。它同时是**定义域**目录名（`tenants/{schema}/agents/{name}/`）；「工作区」另指运行域（session / runs 目录），两者不是一回事。 |
| `description` | 一句话说清「这个 agent 干什么」，会出现在列表里，别人靠它判断用途。 |
| `agent_md` | AGENT.md 全文（见下）。 |

### 2. 写 AGENT.md

用二级标题分段，覆盖：**角色定位、职责范围、行为规范、输出要求**。要点：

- 开头点明「你是谁、负责什么」；
- 写清**默认语言 / 风格 / 尺度**；
- 写清**输出格式**（尤其用户要求「只输出内容」「每次只一个」这类约束要写进去）；
- 写成可执行指令，别写空话；
- 短而准，避免与工具能力无关的常识堆砌。

示例骨架：

```markdown
# 角色

你是一位<定位>，负责<职责>。

## 职责范围

- ...

## 行为规范

- 默认语言：中文
- 风格：...

## 输出要求

- ...
```

**AGENT.md 里可以引用全局变量**：写 `${global_vars.变量名}`（注意是 `${}`，不是流程里的 `{{ }}`），
进驻 system prompt 之前由后端按本租户的值展开。用途是让「环境相关」的常量（地址、区域、账号名）
留在租户配置里，同一份 AGENT.md 装到别的租户只用改那边的变量。

两条要注意：

- 引用了不存在的变量，或**引用了 `secret` 类变量**，都会直接失败并报出变量名 —— 不是警告。
  secret 在保存文档时就会被拒；从资产包装进来的文档绕过了保存校验，加载时也会被拒。
  密钥不要写进 AGENT.md，也不要靠它传递：提示词会进模型上下文与运行记录。
- 资产上架/导出只带走引用名，不带值；装到目标租户后由对方的同名变量填充。名字对不上就装完跑不起来，
  安装前的预检会列出缺哪些。

不确定名字怎么写时，先调 `list_global_vars`（只回名字 / 类型 / 描述，**不回值**），别靠猜。

### 3. 工具与技能：新 agent 默认能读写自己的场地

`create_agent` **不接受**工具参数。新 agent 建出来后**缺省已带一套工具**（管理员可在「设置 → 工具配置」调整）：

- **缺省自带**：`read_file` / `list_files` / `file_exists` / `write_file` / `edit_file` / `delete_file`
  （仅限它自己的会话工作区）+ `list_agents` / `read_agent` / `list_skills` / `read_skill` / `use_skill`
  + `call_agent`（能调谁由「调用授权」决定，没配关系就调不通）。
  `use_skill` 必须在缺省里：技能声明的工具要靠它激活才会注入，少了它普通 agent 就没有点亮 `allow` 授权的入口
  （`read_skill` 是纯查看，不带工具）；
- **仍需另行授权**：`exec` 与 `run_skill_script`（命令执行 / 跑技能自带脚本，两者同属 `builtin:exec`
  组，一次授权同时可用；高风险）、**技能自建**三件套
  （`create_own_skill` / `update_own_skill` / `delete_own_skill`，只能动**它自己的私有** skill）；
- **普通 agent 拿不到的（仅超级智能体，授权也无效）**：`*_shared_skill`（建租户级共享技能）、
  `*_agent_skill`（替别的 agent 建私有 skill）、`bind_skill` / `unbind_skill`、agent 管理类、
  工具与调用授权类。它们不走「工具配置」：勾上也不会出现（目录按身份隐藏），硬调会被执行层拒绝；
- **纯对话类 agent**（只在会话里回答）：**什么都不用做**；
- **需要缺省之外的工具时**：由管理员在该 agent 的「设置 → 工具配置」里授权
  （`必须导入` 恒注入 / `可使用` 被授权后可用 / `拒绝` 显式排除），或开启「不限制工具」。
  **你作为超级智能体也能直接做**：`grant_agent_tool(agent, ref, kind?)` 赋权、`revoke_agent_tool(agent, ref)` 收回、
  `update_agent(agent, unrestricted=…)` 切换开关。`kind` 可选：
  `required`（必须导入，恒注入常驻；即使有技能声明也不降级）、
  `allow`（可使用，按需注入；**必须已有技能声明该依赖**，否则会被拒写 —— 那一行永远不会生效）、
  `deny`（拒绝：即使技能声明也不注入；会覆盖该 ref 原有的行）。
  省略 `kind` 时由系统判定（有声明 → `allow`，没有 → `required`）。每个 ref 只保留一行，改 `kind` 即覆盖旧行，
  返回值会说明覆盖了什么。**ref 从哪来：先 `search_tools(query, agent)` 查**，不要凭记忆写 ——
  它按关键字匹配连接名 / 工具名 / 描述 / ref 片段，返回的就是可直接粘进 `grant_agent_tool` 的 ref
  （`builtin:file` 整组、`mcp:fs` 整条连接、`mcp:fs:read_file` 单个工具），并回填目标已经授过的项（`granted`）。
  查不到就别硬拼：连接名写错时写入只会报 `not found`，`search_tools` 也能用来确认「本租户到底有哪些连接」。
  **给技能补声明**用 `grant_skill_tool(skill, ref)` 追一条、`revoke_skill_tool(skill, ref)`
  撤一条（或 `update_*_skill(tool_deps=[...])` 全量替换）；先 `read_skill(skill)` 看它现在声明了什么。
  目标须处于正常态（废弃一律拒写，**你自己也不例外**）；**给自己加工具是允许的**（你就是管理员），
  改完本 run 的下一轮工具集就会重算，不必等下次会话。改之前先 `read_agent(agent)` 看现状：它会给 `tool_grants`
  （逐条带 `effective` / `pending` / `via_skill` / `note`，说明这条当下生不生效、靠哪个技能点亮），
  以及 `unrestricted` / `discovery`，免得重复授权或误收回还在用的东西；
- **需要技能时**：用 `bind_skill(agent, skill)` 追加、`unbind_skill(agent, skill)` 移除（**仅超级智能体**）
  （也可在管理端「设置 → 调用授权 → 可使用的技能」里配）；只接受**租户级共享** skill 名 —— 私有技能不用绑，
  它天然常驻归属 agent 的提示词。`read_agent` 会回显它当前的 `skills`（已绑定）
  与 `private_skills`（名下的私有技能），以及 `discovery` / `unrestricted` 两个开关是否开启；
  `update_agent(agent, discovery=…)` 可切换「是否允许它发现未绑定的共享技能」；
  `update_agent(agent, is_supervisor=…)` 授予 / 撤销**超权**（与 超级智能体同级：管理类工具 + 跨域写），
  仅 supervisor 可操作，且不能降级租户内最后一个 supervisor。
  提示词里只有名字 + 描述（只是清单）；正文按需 `read_skill`，**工具要 `use_skill` 才注入**。
  废弃(3) 时该 agent 名下的私有技能与 `AGENT.md` 等自身定义一律冻结（只有正常态可写）。
  你自己调 `list_skills` 看到的是**全量租户级技能**（忽略白名单），不会出现「改得到却列不出」。
- **需要它能调用别的 agent 时**：`allow_agent_call(agent, target)` 加一条、`revoke_agent_call(agent, target)` 收回
  （**仅超级智能体**；也可在管理端「设置 → 调用授权」里配）。调用授权就是 `agent_relations` 里的一行 —— 没这行 `call_agent`
  一律拒绝，并且**没有任何 agent 能绕过它**：连你自己想调用别人，也得先写这一行（写这行本身只有你能做）。
  被调用方可以是任意未废弃的 agent（**含超级智能体** —— 它被调起时仍是完整的管理面，恒读草稿）；
  `read_agent` 能看到目标的 `can_call` / `called_by` 现状。

所以：**不要在回复里承诺新 agent 具备缺省之外的工具能力**，需要额外的如实说明"还要在工具配置里授权"。

### 4. 运行时能看到哪些目录

工具不是只有「工作区」一个视野。三类执行工具的挂载是固定的：

| 工具 | `/workspace` | 实体目录（**只读**） |
|---|---|---|
| `exec` | 本次运行的工作区，读写，同时是 cwd | `/agent` = 本 agent 定义域（`AGENT.md`、私有 skills） |
| `run_skill_script` | 同上 | `/skill` = 该技能目录 |
| `custom__*`（自定义工具） | 同上 | `/tool` = 该工具目录 |

- `/agent` 只读：`cat /agent/AGENT.md`、读私有 skill 的附件都可以，**写不进去**；
- **脚本不要放定义域**：放进去能跑，但 `mode: none` 下 `/agent/...` 这类路径不会被翻译（只翻译
  argv 与 cwd），表现是「本地调试找不到文件、线上正常」。要反复跑的脚本放技能的 `scripts/`，
  用 `run_skill_script`；一次性脚本落工作区用 `exec`；
- 三类工具都**看不到**其它实体目录（别的 agent、别的技能、别的自定义工具）。

### 5. 调用 `create_agent`

参数：`name`、`agent_md` 必填；`description` 可选（建议给）；`is_supervisor` 可选（默认 false，
建成即带超权 —— 仅 supervisor 可用，别随手给：拿到就是 超级智能体同级）。

### 6. 建完自检

- 名字合法、没和已有 agent 重名（先 `list_agents` 看一眼）；
- 描述能让别人判断用途；
- AGENT.md 里的输出约束和用户诉求一致；
- 需要工具 / 技能时，明确告知用户去哪配（「工具配置」授权、「可使用的技能」绑定）；并核一遍生效三件套：
  **共享技能已绑定** + 该 agent 有对应工具的 `allow` 授权 + 运行时 `use_skill` 激活 —— 缺任何一个，工具都不会出现。

## 注意事项

- **不要用「删了重建」改配置**：`create_agent` 遇到同名会失败，但改名字 / 描述 / `discovery` / `unrestricted` 用 `update_agent` 即可；改 **AGENT.md 正文**用文件工具直写 `write_file("/agents/<name>/AGENT.md", 全文)`（或 `edit_file` 局部改）。删了重建会让 **ID 变**，还会丢掉已绑定的技能，尽量避免。
- **`update_agent` 对「正在运行的自己」只禁一件事：改名**（`agent` 传自己 + `new_name` 会被拒 ——
  改名会让本轮后续文件操作失效，纯技术原因）。**描述 / `discovery` / `unrestricted` / `is_supervisor` 对自己同样生效**，
  不必去管理端；改自己的 `AGENT.md` 正文用 `write_file("/agents/<自己>/AGENT.md", 全文)`。
- **`main`（预置超级智能体）与三个内置技能受保护**：`delete_agent("main")`、给 `main` 改名会被拒（报错含 `protected agent`）；`skill-authoring` / `agent-authoring` / `custom-tool-authoring` 同样不可删除、不可改名（正文与描述仍可改）。这是与「正常态」状态闸**正交**的另一道闸 —— 报 `protected` 就别再换参数重试或试图先改状态。
- **你作为超级智能体可以跨 agent / skill 写文件**：文件工具路径以 `/` 开头即从**租户根**算 ——
  `/agents/<name>/AGENT.md`、`/skills/<name>/SKILL.md`、`/agents/<name>/skills/<name>/SKILL.md`。
  写前先 `read_file` 读原文。写闸有两个判据，别混：
  **agent 自身定义**（`AGENT.md`、描述等）要求该 agent **正常态**（废弃会被拒）；
  **agent 私有 skill 目录内的文件**同样要求**归属 agent** 是正常态（废弃一律冻结），
  外加该 skill 自身正常态。**你是超级智能体也不放宽**。
  注意 `/` 前缀**不是你独享的**：普通 agent 也能用它访问**自己的**私有 skill 目录
  （`/agents/<自己>/skills/<技能名>/...`，含附属文件与子目录，同样过上面那两道闸），
  其余跨域路径仍然只有你能走。普通 agent 连**自己的 `AGENT.md`** 都读写不到
  （不在它的可达范围内），要改它的 AGENT.md 只能由你或管理端来。
- **实体本身仍走元工具**：新建 / 删除 / 改名 agent 用 `create_agent` / `delete_agent` / `update_agent`；skill 则分三套 —— `*_own_skill` 动**自己的私有** skill、`*_shared_skill` 动**租户级共享** skill、`*_agent_skill` 动**指定 agent 的私有** skill（工具名即归属，拿错会被拒，`skill_id` 可精确定位）。别用文件工具去新建 / 删除实体根目录。
- **重试只对一种报错成立**：仅当报错文本确为 `pq: invalid input syntax for type uuid` 这类平台写库异常时，才可原样重试 1–2 次（它换名字、换参数都一样报错）。**其它报错不要重试** —— 重名、名字非法（下划线 / 以横线开头）、目标已废弃、授权不足都是确定性失败，重试只是浪费时间，应把真实报错如实告知用户。
- **新 agent 默认只有上面那套工具**：`exec` 与**技能自建**（`*_own_skill`）需另行授权；而创建**共享**（租户级）skill、替别的 agent 建私有 skill、技能绑定、agent 管理与工具/调用授权都是 **超级智能体专属** —— 给普通 agent 授权也拿不到。所以不要承诺它具备缺省之外的能力，也不要把「去工具配置里授权」当成万能答法。
- **诚实**：调用完把工具真实返回告诉用户；若实际是自己代写而非调用子 agent，不要谎称「已让某 agent 生成」。
