# Skill 编写指南

## 用途

指导你创建 / 修改 / 删除 skill，并写出高质量的 `SKILL.md`。
带**可执行脚本**的技能，还要把脚本放对位置，并在正文里讲清怎么调用它。

## 何时使用

出现这些意图时：

- 「把这个流程 / 规范存成 skill」、「以后都这么做」
- 「写个 skill 来做 X」、「把这个脚本 / 这段代码存成 skill」
- 「改一下 / 删掉某个 skill」、「现在有哪些 skill？」

**只做一次的事不要建 skill** —— 直接做即可。

## 什么是 skill

skill 由**两部分**组成（后者可选）：

| 部分 | 位置 | 必选 | 作用 |
|---|---|---|---|
| `SKILL.md` | 技能目录根 | 是 | 知识正文：这件事该怎么做 |
| `scripts/` | 技能目录内 | 否 | 配套的**可执行脚本**，入口固定 `scripts/main.py` |

由此有两种形态：

- **纯文档技能** —— 只有正文（规范、流程、模板）；
- **可执行技能** —— 正文讲「怎么用」，代码在 `scripts/`，agent 用 `run_skill_script`
  **直接运行**，**不需要**把代码抄到工作区。

目录形态（相对 `agent.root_dir`）：

```
tenants/{schema}/skills/{name}/                    共享（租户级）
├── SKILL.md
└── scripts/main.py                                ← 可选；不传 script 时的默认入口

tenants/{schema}/agents/{agent}/skills/{name}/     私有（agent 级）
├── SKILL.md
└── scripts/…
```

每个 skill 有**归属**，由 `agent_id` 决定：

| 归属 | 内容位置（相对 agent.root_dir） | 生效方式 |
|---|---|---|
| **共享**（租户级） | `tenants/{schema}/skills/{name}/SKILL.md` | 只有**绑定**到某个 agent 后才会进入它的提示词；可见性另由「全员可见 / 白名单」控制 |
| **私有**（agent 级） | `tenants/{schema}/agents/{agent}/skills/{name}/SKILL.md` | 归属 agent 天然拥有（常驻其提示词），仅它可见、可改删 |

同名时**私有覆盖共享**。

**共享 ≠ 自动生效**：共享 skill 建出来后是**收窄**状态（`restricted = true`，白名单只含创建者），
需要在 agent 的「设置 → 调用授权 → 可使用的技能」里绑定，它才会写进那个 agent 的提示词；
没绑定它的 agent **看不到、也改不了**它。**超级智能体可以直接做这两件事**：`bind_skill` / `unbind_skill`
增删白名单，`update_shared_skill(..., restricted=false)` 放开为全员可见（`restricted=true` 收回白名单模式，
白名单数据保留、不用重建）；`read_skill` 会回显当前的 `restricted`、`bound_agents`，
以及它声明的工具依赖 `tool_deps`（只展示，不激活）。

## 选共享还是私有

判据：**这条知识是所有 agent 都用，还是只有特定 agent 用？**
私有与共享是**两套工具**，选哪个工具就是选归属：

| 场景 | 用什么工具 |
|---|---|
| 元能力（造 skill、造 agent、改系统）、某个 agent 岗位专用且**它自己能维护** | `create_own_skill`（自己的私有；**需管理员先授权**） |
| 业务领域知识、通用规范（报告格式、提 PR 流程等） | `create_shared_skill`（租户级共享，**仅超级智能体** 可用） |
| 某个 agent 岗位专用，但**由超级智能体代建**（对方没被授权，或不便自己建） | `create_agent_skill`（归属指定 agent 的私有，**仅超级智能体** 可用） |

两条硬约束：

1. **skill 的可用范围应与工具可用范围一致** —— 一条技能要用到的工具，得先确认目标 agent 确实拿得到，再把它绑定过去；
2. **绑定才注入** —— 进 system prompt 的是"绑定关系"（每条约几十至上百字符），与共享/私有无关。
   判不准时优先私有（私有天然只进归属 agent 的提示词），或共享 + 只绑给真正需要的 agent。

**默认私有；拿不准时也用私有** —— 私有随时能改成共享，共享扩出去后要逐个 agent 收绑定才干净。
非超级智能体只能用私有那一套（`*_own_skill`，且**需管理员先授权**）；需要共享 skill 时请超级智能体建，再 `bind_skill` 绑定。
超级智能体还能用 `*_agent_skill` 直接替某个 agent 建 / 改 / 删它的私有技能 —— **无需**给那个 agent 授权 `*_own_skill`。

## 代码放正文，还是放 `scripts/`？

**判据：超过约 20 行、或需要 `import`、或需要参数解析 —— 一律放 `scripts/`。**

| 形态 | 适用 | 代价 |
|---|---|---|
| 内嵌正文 | 三五行的片段 / 一次性示意 | agent 每次都要抄到工作区再跑 → 正文与副本**会分叉**；参数经 shell，有引号坑 |
| `scripts/main.py` | 完整可运行的程序（`import` / `argparse` / 错误处理 / 多函数） | 需要宿主 agent 被授权 `builtin:exec`；写它的人受限（见下表） |

优先 `scripts/` 的三个理由 —— `run_skill_script` 就是为此而设：

- **代码只有一份**：技能目录只读挂载到 `/skill`，不存在「正文一份、工作区一份」的分叉；
- **参数不经 shell**：`args` 是**字面量 argv 数组**逐字传给脚本，没有分词 / 引号 / 转义问题
  （`exec` 走 shell，Windows 下 `cmd` 会吃掉引号，`python -c "…"` 常表现为 exit 1 且无输出）；
- **省轮次**：少一次 `write_file`，且不用每次重抄。

### 脚本的硬约定

| 约定 | 内容 |
|---|---|
| 入口 | `scripts/main.py` —— `run_skill_script` 不传 `script` 时的默认值 |
| 扩展名 | 只支持 `.py` / `.js` / `.lua` / `.sh`；**扩展名决定解释器**（命令名 `python` / `node` / `lua` / `sh`，**不能**指定绝对路径或版本） |
| cwd | 工作区根（沙箱内 `/workspace`）。引用**工作区**里的文件用相对路径；引用**技能目录**里的文件见下方「脚本里怎么引用技能目录内的文件」 |
| 挂载 | `/skill` **只读**（技能目录）、`/workspace` **读写**（同时是 cwd）；**不挂** `/agent` |
| 参数 | 由 `args` 数组逐字传入（argv），不是一整条命令行字符串 |
| 路径 | `script` 必须落在技能目录内 —— 工具会拒绝任意路径（跑任意路径是 `exec` 的事） |
| 附属模块 | 放 `scripts/lib/`，用**绝对 import**（`from lib.x import y`）；**禁止** relative import。这是 **Python** 的写法，其它语言见下一节 |

跑法：

```
run_skill_script(skill="xlsx-report", args=["--input", "in.xlsx", "--min-amount", "1000"])
```

### 脚本里怎么引用技能目录内的文件

脚本的启动形态是 `[解释器, /skill/<script>]`，**cwd 是 `/workspace`** —— 所以「引用技能目录里的
文件」与「引用工作区里的文件」是两套路径，别混：

| 语言 | 引用技能目录内的文件 | 说明 |
|---|---|---|
| Python | `Path(__file__).parent / "lib/x.py"`；模块用 `from lib.x import y` | `/skill/scripts` 在 `sys.path[0]`，同目录可直接 import |
| Node | `require("./lib/x.js")` | 相对**引用它的文件**解析，与 cwd 无关 |
| Lua | `dofile(SKILL_DIR .. "/lib/x.lua")`，或先 `package.path = SKILL_DIR .. "/?.lua;" .. package.path` 再 `require("x")` | **只写 `require("x")` 会失败**：默认的 `./?.lua` 相对 cwd（工作区） |
| Shell | `. "$SKILL_DIR/lib.sh"` | `source` 的相对路径同样跟着 cwd |

`SKILL_DIR` 一律从**脚本自身位置**推导，不要硬编码 `/skill`：

```python
from pathlib import Path
SKILL_DIR = Path(__file__).resolve().parent.parent      # scripts/ 的上一级
```

```javascript
const SKILL_DIR = require("path").resolve(__dirname, "..")   // Node（CJS；ESM 用 import.meta.dirname）
```

```lua
local SKILL_DIR = (arg and arg[0] or ""):match("^(.*)[/\\]scripts[/\\]") or "."   -- Lua
```

```sh
SKILL_DIR=$(cd "$(dirname "$0")/.." && pwd)             # Shell
```

原因：`/skill` 只在 nsjail / docker 下真实存在。`mode: none` 不做隔离，挂载表只翻译 argv 与
cwd —— 代码里写死的 `/skill/...` 在本地调试会「文件不存在」，而线上却正常，这类分叉最难查。

三条附加约束：

- **`scripts/` 是代码、`templates/` / `references/` 是数据**：都在 `/skill` 下、都只读，但只有
  `scripts/` 里的那几个扩展名（`.py` / `.js` / `.lua` / `.sh`）会被 `run_skill_script` 当可执行脚本；
- **`/skill` 只读**：产物一律写 `/workspace`（cwd）或其子目录，别试图落进技能目录；
- **Shell 的 `.` 是致命的**：`sh` 里 `. 不存在的文件` 会**直接终止整个脚本**（不是报错继续），
  症状是「没有任何输出、也没退出码可看」—— 相对路径写错的代价比想象中大，一律写 `SKILL_DIR` 绝对路径。

### 写 `scripts/` 的权限

`create_*_skill` **只写 `SKILL.md` 正文**，脚本要**另外**用文件工具写：

| 技能归属 | 谁能写脚本 | 路径写法 |
|---|---|---|
| 共享（租户级） | **仅超级智能体** | `write_file("/skills/<name>/scripts/main.py", …)` |
| 私有（agent 级） | 归属 agent 自己，或超级智能体 | `write_file("/agents/<自己>/skills/<name>/scripts/main.py", …)` |

写侧闸门（两档同口径）：**目标 skill 必须正常态**；**归属 agent** 也须是**正常态**（废弃一律冻结）。
超级智能体也按同一判据判**归属 agent**，不因身份放宽。

## SKILL.md 怎么写

固定四段（二级标题 `##`），只写必要信息：

- `## 用途` —— 一句话说明这个 skill 解决什么问题
- `## 何时使用` —— 什么情况下该读它；触发条件要具体、可识别
- `## 怎么做` —— 步骤 / 规则 / 模板，这是主体
- `## 注意事项` —— 容易踩的坑、边界条件

写作要求：

- **开头就写「何时使用」** —— agent 靠这段决定要不要读；
- **写成可执行指令** —— 写「运行 `go test ./...`」，别写「要做测试」；
- **给模板与示例** —— 比大段描述更省 token、也更好用；
- **不写常识** —— 只写模型不知道或容易做错的；
- **短**。skill 是按需读取的知识，越长越贵。

**正文里可以引用全局变量**：写 `${global_vars.变量名}`（注意是 `${}`，不是流程里的 `{{ }}`），
`read_skill` / `use_skill` 取到的正文由后端按本租户的值展开。用途是让「环境相关」的常量
（接口地址、区域、账号名、库表名）留在租户配置里，同一份技能装到别的租户只用改那边的变量。

四条要注意：

- **只有 `SKILL.md` 会被展开**：`scripts/` 里的脚本、`references/` / `templates/` 里的附属文件原样落地，
  写 `${global_vars.X}` 没人替换 —— 脚本要取环境值，让它读环境变量，或由调用方经 `args` 传进来；
- 引用了不存在的变量，或**引用了 `secret` 类变量**，读取正文时会直接失败并报出变量名 —— 不是警告；
  密钥不要写进正文：正文会进模型上下文与运行记录；
- 资产上架 / 导出只带走引用名、不带值；装到目标租户后由对方的同名变量填充，名字对不上就装完读不出来；
- **`create_*_skill` / `update_*_skill` 写正文时不校验引用**（管理端文件接口才会拦 secret），
  写错了要到第一次 `read_skill` 才炸 —— 建完顺手读一次最省事。

不确定名字怎么写时：超级智能体可直接调 `list_global_vars`（只回名字 / 类型 / 描述，**不回值**）；
普通 agent 拿不到这个工具（仅超级智能体），别硬猜，问管理员要准确名字。

**带脚本时，正文必须讲清三件事**（这是可执行技能与纯文档技能的唯一差别）：

1. **脚本做什么** —— 一句话，别复述代码；
2. **怎么调** —— 给出 `run_skill_script` 的完整示例，含典型 `args`；把参数含义列成表；
3. **输入 / 输出约定** —— 读哪个文件、产物写到哪里、失败怎么表现。

**别把脚本代码贴进正文** —— 代码在 `scripts/` 里，正文只讲怎么用。
正文贴代码会产生两份副本并分叉：改了脚本忘了改正文，读到正文的 agent 会照着过期代码行动。

## 命名规范

必须匹配 `^[a-zA-Z0-9][a-zA-Z0-9-]*$`，且不超过 64 字符：

- 字母、数字、横线；**不能**下划线、**不能**以横线开头；
- 用小写 + 横线：`git-pr-flow`、`report-format`；
- 一眼看出用途：`code-review` 优于 `cr`、`helper`。

## 用什么工具

**私有**（自己的，只有本 agent 可见）——**需管理员为该 agent 授权 `*_own_skill`** 后才能用：

| 目的 | 工具 | 关键参数 |
|---|---|---|
| 新建 | `create_own_skill` | `name`、`skill_md` **必填**；`description` 可选 |
| 改内容 / 描述 / 改名 | `update_own_skill` | `name` **必填**（定位用）；`skill_md`、`description`、`new_name` 按需 |
| 删除 | `delete_own_skill` | `name`（或 `skill_id`） |

**共享**（租户级共享）——**仅超级智能体** 可用：

| 目的 | 工具 | 关键参数 |
|---|---|---|
| 新建 | `create_shared_skill` | 同 `create_own_skill`；建后默认只授权你自己，要给别人用再 `bind_skill` |
| 改内容 / 描述 / 改名 | `update_shared_skill` | 同 `update_own_skill`；改动会影响**所有**绑定它的 agent |
| 删除 | `delete_shared_skill` | 同 `delete_own_skill` |

**指定归属**（替某个 agent 建它的私有 skill）——**仅超级智能体** 可用：

| 目的 | 工具 | 关键参数 |
|---|---|---|
| 新建 | `create_agent_skill` | `agent`、`name`、`skill_md` **必填** |
| 改内容 / 描述 / 改名 | `update_agent_skill` | `agent`、`name` **必填**；`skill_md`、`description`、`new_name` 按需 |
| 删除 | `delete_agent_skill` | `agent`、`name`（或 `skill_id`） |

**共享 / 代建可声明工具依赖（`tool_deps`，仅超级智能体）**：`create_shared_skill` / `update_shared_skill` /
`create_agent_skill` / `update_agent_skill` 多一个可选参数 `tool_deps` —— 该技能要用到的工具 ref 列表
（如 `builtin:exec`、`builtin:file`）。全量替换：传 `[]` 清空，不传则不改。它**不产生授权**，只是把宿主 agent
**已有的** `allow` 授权「点亮」；同时决定 `grant_agent_tool` 写 `allow`（按需注入、省上下文）还是
`required`（恒注入）。`*_own_skill` 系列**不接受**该参数（传了会显式报错）。

**改单条依赖用增量工具（仅超级智能体）**：`grant_skill_tool(name|skill_id, ref)` 追加一条、
`revoke_skill_tool(name|skill_id, ref)` 移除一条。二者幂等，而且**只动这一条** —— 不像
`tool_deps` 那样全量替换（那个不传 = 不动、传 `[]` 才清空）。返回值带变更后的完整
`tool_deps`，不必再 `read_skill` 一次。

`ref` 写法与 `grant_agent_tool` 一致：`builtin:<工具名>` / `builtin:<类别>` /
`mcp:<连接>[:<工具>]` / `rpc:…` / `local:rpc[:<端点>]`；**不接受自定义组**（`custom:*` 会被拒）。
目标 skill 须处于正常态（废弃一律拒写），其归属 agent 也须是正常态（废弃则它名下的私有技能冻结）；
改名不归它们管，走 `update_*_skill` 的 `new_name`。
移除时**不做存在性校验** —— 工具/连接被删后留下的死 ref 照样能清掉，这是有意的。

> **技能带脚本时，`tool_deps` 必须声明 `builtin:exec`** —— `run_skill_script` 属于该类别组，
> 宿主 agent 没被授权时技能建了也跑不起来（调用会被明确拒绝）。声明依赖本身不产生授权，
> 仍需管理员或超级智能体用 `grant_agent_tool(agent, "builtin:exec")` 真正授权；
> 而且**该技能必须已绑定给宿主 agent**（`run_skill_script` 只认「注入给我」的技能：私有 ∪ 已绑定）。

**通用**：

| 目的 | 工具 | 关键参数 |
|---|---|---|
| 看有哪些可用 | `list_skills` | 无 |
| 读某个的全文（**不激活**） | `read_skill` | `name`（或 `skill_id`，按 id 定向仅超级智能体）；回显 `tool_deps` 与可见性 |
| 用某个（**激活**它的工具依赖） | `use_skill` | `name`（或 `skill_id`）；返回同样的正文 + `tool_deps`，其依赖从下一轮推理起注入 |
| 绑定 / 解绑（白名单） | `bind_skill` / `unbind_skill` | `agent`、`skill`（**仅超级智能体**，幂等） |

`list_skills` 只列当前**可见范围**（私有 + 已绑定；该 agent 开了 discovery 时再加「全员可见」的共享技能），
单次最多 100 条（`truncated=true` 时按 note 缩小范围）；**超级智能体视角是全量租户级**（忽略白名单）。

工具名即归属：`*_own_skill` 只动你自己的私有 skill，`*_shared_skill` 只动租户级共享 skill，
`*_agent_skill` 只动**指定 agent** 的私有 skill，拿错工具会被明确拒绝（不会静默改到另一个空间）。
反过来也一样严格：`scope` 参数**已下线**，传了会显式报错并提示改用对偶工具；`restricted`
只有 `update_shared_skill` 接受，传给 `*_own_skill` / `*_agent_skill` 同样显式报错。

## 创建流程（四步，缺一不可）

| 步 | 动作 | 漏了会怎样 |
|---|---|---|
| 1 | `create_shared_skill` / `create_own_skill` / `create_agent_skill` —— **只写 `SKILL.md`** | —— |
| 2 | 带脚本时：用 `write_file` 把脚本写进 `<技能目录>/scripts/`（**路径必须以 `/` 开头**，写法见上方「写 `scripts/` 的权限」表） | 正文教人跑脚本、脚本却不存在；**创建照样成功**，直到运行时才炸 |
| 3 | **冒烟测试**：立刻 `run_skill_script` 跑一遍，并**回读产物**验证 | 缺陷留到用户第一次使用时才暴露 |
| 4 | 要给别人用：`bind_skill` + `grant_agent_tool(agent, "builtin:exec")` | 技能在库里，但那个 agent 看不到 / 跑不起来 |

**第 1 步不会建 `scripts/` 目录**（实测：建完 `list_files` 只有 `SKILL.md`），也**没有任何校验** —— 正文里写「运行 `scripts/main.py`」而脚本没写，创建依然返回成功。所以第 2、3 步必须自己做。

**冒烟测试至少覆盖三点**：

1. 正常参数跑通；
2. **回读产物**确认内容正确 —— `run_skill_script` 回 `stdout` / `stderr` / `exit_code` / `truncated` 四项，
   退出码 0 **不等于**产物对；报错先看 `stderr`（输出过长被截时 `truncated=true`）；
3. 一个边界（非法参数、不存在的输出目录、空输入等）。

看了 `stderr` 仍定位不出来时，再用 `exec` 把脚本落盘成 `.py` 单独跑一遍，重定向到日志后 `read_file` 看。

## 注意事项

- **不要手动往目录里写 `SKILL.md`** —— 那样表里没有记录，`list_skills` 看不到它，会变成「孤儿文件」。一律走 `create_own_skill`（共享则 `create_shared_skill`、代建则 `create_agent_skill`）/ `update_*_skill` / `delete_*_skill`。
- **正文进 `skill_md`，附属文件要另写**：`*_own_skill` 系列只写 `SKILL.md` 正文。脚本 / 模板 / 示例 / 清单这类**附属文件**，要另行用文件工具按 `/` 前缀写进技能目录（路径与权限见「写 `scripts/` 的权限」）。注意 `/` 前缀对普通 agent **只能到自己的私有 skill 目录**：别人的 agent / skill、租户级共享 skill、自己的 `AGENT.md` 都不可达（共享技能的附属文件只有超级智能体能写）。
- **附属文件分两类，别混为一谈**：
  - **被动数据**（模板 / 示例 / 清单 / 语料）—— 运行时确实不会自动读，是「死文件」：要么把关键内容写进正文，要么在正文里写明它的路径与用途，让读到的人在需要时去打开；
  - **可执行脚本**（`scripts/*.py|*.js|*.lua|*.sh`）—— **不是死文件**：`run_skill_script` 会挂载技能目录并直接运行它，扩展名决定解释器。这类文件的存在与用法**必须写进正文的「怎么做」**。
- **`*_own_skill` 不是每个 agent 都有**（缺省工具集**不含**技能自建）：拿不到工具就别硬试 —— 要么请管理员在「工具配置」里给该 agent 授权，要么请超级智能体用 `create_shared_skill` 建共享 skill 再 `bind_skill` 绑给它。
- **改名会连整个技能目录一起搬**：`update_own_skill` / `update_shared_skill` 传 `new_name` 时，目录（含附属文件与子目录）整体迁移，你不用管。
- **不传 `skill_md` 时只改元数据**，内容保持不变。
- **`tool_deps` 只在共享 / 代建上有**（仅超级智能体）：私有 `*_own_skill` 传它会显式报错。改单条用 `grant_skill_tool` / `revoke_skill_tool`，全量替换用 `update_*_skill(tool_deps=[...])`。三个「不等于」要分清：
  - **声明依赖 ≠ 授权**：宿主 agent 没有对应的 `allow` 授权时，声明了也不会注入；
  - **绑定 ≠ 注入**：绑定只让技能进提示词清单，不产生工具候选；
  - **`read_skill` 看到依赖 ≠ 已激活**：依赖只在 `use_skill` 之后（下一轮起）生效，本 run 结束失效。
- **`run_skill_script` 需要文件系统存储**（`local` / `nfs`）：技能目录要被挂进沙箱，S3 存储下宿主路径不可达，会直接报「技能目录在宿主上不可达」（脚本本身没问题，别去改脚本）。
- **共享 skill 的改删要谨慎（仅超级智能体）** —— 它可能正被别的 agent 绑定使用；改名/改内容会影响所有绑定它的 agent。
- **`SKILL.md` 入口文件不能单独删**：`delete_file` 在根寻址下会拒删 `SKILL.md`（删掉会留下「登记在库、没有正文」的空技能，运行时读不到正文会静默降级为空内容）。删整个技能请用 `delete_own_skill` / `delete_shared_skill` / `delete_agent_skill`。
- **`skill-authoring` / `agent-authoring` 是系统基石：不可删除、不可改名**（正文与描述仍可改）。同口径还有 `main` 这个预置超级智能体（不可删 / 不可改名；它可以被其它 agent 调用，被调起时恒读草稿）—— 这在状态闸之外，报错里写 `protected` 就别再试。
- **system prompt 里只出现 name + description**，正文要 `read_skill` 才读到。所以 `description` 必须写清「什么时候该读我」。
- **建完自检**：
  1. 名字合法？工具选对（私有还是共享）？
  2. `description` 能让别的 agent 判断该不该读？正文的「何时使用」够具体？
  3. **带脚本吗**？带的话：脚本写进 `scripts/` 了吗（不是贴在正文里）？`tool_deps` 声明 `builtin:exec` 了吗？正文讲清「脚本做什么 / 怎么调 / 输入输出约定」了吗？宿主 agent 被授权 `builtin:exec`、且已绑定该技能了吗？
