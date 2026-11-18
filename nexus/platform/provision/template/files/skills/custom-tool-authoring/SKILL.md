# 自定义工具编写指南

## 用途   

把「一段固定的业务逻辑」落成一个**租户级自定义工具**（对外工具名 `custom__<name>`）：
模型给参数、脚本算结果、返回结构化 JSON。

本技能负责**判断该不该做**、**选语言**、**设计元数据与参数**、**怎么写代码**、**怎么验证**。

## 两条落地路径

| 角色 | 怎么做 | 形态 |
|---|---|---|
| 超级智能体 | `create_custom_tool` → `write_file` → `grant_agent_tool` | 全自动，三步闭环 |
| 人 | 「工具 → 自定义工具」页填表 + 贴代码 | 表单字段与下面第 1 / 第 2 段一一对应 |

超级智能体的三步，**顺序不能变**：

1. **`create_custom_tool`** —— 建实体：契约文件 `tools/<name>/TOOL.json` + 一个可跑的最小骨架。
   参数里给 `name` / `description` / `display_name` / `lang` / `entry` / `parameters` / `output` / `config_fields` / `configs` / `timeout_ms` / `max_output_bytes` / `egress`。
2. **`write_file("/tools/<name>/<entry>", "<代码全文>")`** —— 写真正的实现，覆盖骨架。
   entry 缺省按语言取 `main.py` / `main.js` / `main.lua` / `main.sh`。
3. **`grant_agent_tool(agent="<目标>", ref="custom:<name>")`** —— 授权给要用它的 agent。

**契约在 `TOOL.json` 里，与代码同目录**（磁盘优先于库里那份）：描述 / 参数 schema /
语言 / 入口 / 限制 / 配置声明都写在这里，改名或改 schema 都请走 `update_custom_tool`
（它会同步写文件），不要手改库。

**第 3 步漏了 = 模型看不见这个工具。** 建完不授权时，目标 agent 的工具目录里根本不会出现它，
模型也就永远不会调用（自省用 `list_custom_tools`，那是超级智能体的活）。

维护已有工具：

| 要改什么 | 用什么 |
|---|---|
| 描述 / 参数 schema / 语言 / 入口 / 超时 / 出网 / 配置 | `update_custom_tool`（只传要改的字段） |
| 代码内容 | `write_file` 直接改 `/tools/<name>/<entry>`（前导 `/` 不能漏，见第 2 步） |
| 启用 / 锁定 / 废弃 | **管理端**（状态切换不走元工具） |
| 删除 | `delete_custom_tool`（仍被授权 / 依赖 / 组引用时默认拒绝，列出引用方；确认删再传 `force=true`） |

**工具的存在性以 `tools/<name>/TOOL.json` 为准**（目录即实体）：把一整个工具目录拷进
`tools/` 就算存在（缺省正常态、无配置），目录没了就等于删除。反过来，只改库不写文件
是不生效的 —— 描述与参数以文件为准。

非超级智能体的 agent 没有这几个元工具（分类 `custom-tool`，仅超级智能体），只能把代码交给用户去「工具 → 自定义工具」落地。

## 何时使用

出现这些意图时：

- 「写个工具来查 / 算 / 转换 X」「让 agent 自己能调这个脚本」
- 「这段脚本每次都贴一遍，能不能存成工具」
- 「自定义工具跑不通」：输出不是 JSON、超时、缺配置、出网被拦

**先做选型，别默认选自定义工具**：

| 需求形态 | 用什么 | 判据 |
|---|---|---|
| 一次性任务 | 直接做（`exec` / 现有工具） | 只跑一次，不需要复用 |
| 固定逻辑、模型给参数、结果结构化 | **自定义工具** | 纯计算 / 查表 / 调 HTTP，输入输出清晰 |
| 需要读写会话文件、跑任意命令 | 内置 `read_file` / `exec` | 自定义工具是「一个函数」，不是另一个 shell |
| 知识、流程、规范（没有代码） | skill（先读 `skill-authoring`） | 不需要可执行 |
| 技能自带的可执行脚本 | skill 的 `scripts/` + `run_skill_script` | 跟技能同生命周期，不单独授权 |
| 一份代码服务多个租户、要按租户配 key / 多实现 | 插件 | 自定义工具**只属于本租户** |

一句话：**要授权给 agent 用、要进工具目录、要参数 schema —— 才是自定义工具。**

## 第 0 步：选语言

支持 `python` / `js` / `lua` / `sh`，但**能不能跑由部署决定**，不是代码保证：

| 语言 | 走哪条路 | 注意 |
|---|---|---|
| `python` | sandboxd 的 `langs.python` | **默认选它**：绝大多数环境都有 |
| `js` | `langs.js` | 镜像里的命令通常叫 `node`，语言名仍写 `js` |
| `lua` | `langs.lua` | 最容易被漏装；有的部署要求绝对路径（如 `lua55`） |
| `sh` | 不经 `langs`，直接 `sh <entry>` | 只要沙箱有 `/bin/sh` 就能跑；只适合简单命令 |

判据：

1. **默认 python**，除非有明确理由换；
2. 换之前先确认环境装了：「工具 → 自定义工具」页顶显示「沙箱 xxx · 可执行语言 a / b」，语言下拉里不可用的会标「（环境不可用）」；
3. 页顶显示「沙箱能力未知」时**不要猜**：先让 sandboxd 起来，或直接用 `exec` 跑一次 `node -v` / `lua -v` 确认；
4. `mode=none` 下配置错语言会**启动就失败**（宿主 LookPath 校验）；`nsjail` / `docker` 下不校验，错误要等到第一次调用 —— 所以别凭「服务起来了」判断语言可用。

## 硬约束（先看，避免白干）

| 约束 | 细节 |
|---|---|
| 名称 | 只允许字母、数字与短横线（`sum-sales`），不能以短横线开头；**不能含 `__`**（对外名是 `custom__<name>`）；长度 ≤ 64；**创建后不可改**（改名会让 `custom:<name>` 的授权静默失效） |
| 写代码的路径 | 必须是 `/tools/<name>/<entry>`：**前导 `/` = 从租户根算**。少了它会被当成工作区相对路径 —— 文件写进会话工作区、工具仍读 create 时生成的骨架，表现为「改了代码却跑旧逻辑」 |
| 生效三件套 | `status = 1` **且** 目标 agent 被授权 `custom:<name>` **且** 平台开通 `sandbox.exec`。缺一个：目录置灰、执行被拒 |
| 运行环境 | 沙箱内 cwd 是 `/workspace`（本次运行的工作区，可写）；工具目录只读挂在 `/tool` |
| 出网 | 默认断网；要开 `egress` **并且**平台开通 `net.egress` 才通 |
| 超时 | 工具 `timeout_ms` 与本次运行上限**取小**；最大 600000 |
| 输出 | `stdout` 必须是**一段合法 JSON**；超过 `max_output_bytes`（默认 65536，上限 1048576）**直接判失败**（不截断） |
| 日志 | 一律写 `stderr`（同样限长） |
| 密钥 | 配置里的 `secret` 项回显打码；脚本拿得到明文，但输出里出现**字符串型且长度 ≥ 8** 的配置值会被替换成 `••••`，并给结果加 `_masked: true`（太短的值不遮，避免误伤正文，别指望它兜住短 key，自己就不要往结果里塞） |

## 给人抄的时候，按这三段交付

1. **元数据表** —— 可逐字段照抄进表单；
2. **代码全文** —— 一个完整可运行的文件（不是片段，不要 `...`）；
3. **验证清单** —— 建完之后按顺序确认哪些点。

超级智能体自己落地时不用**输出**这三段，但顺序是一致的：先定元数据再写代码，最后照清单验一遍。

---

## 第 1 段：元数据

| 字段 | 填什么 |
|---|---|
| `name` | `sum-sales`（字母数字短横线，创建后不可改） |
| `display_name` | 面板短标题，可空（**不影响模型**，模型只看 name + description） |
| `description` | **给模型看**：一句话说清「什么时候该用、什么时候别用」 |
| `lang` / `entry` | `python` + `main.py`（或 `js`/`main.js`、`lua`/`main.lua`、`sh`/`main.sh`） |
| `parameters` | 入参 JSON Schema（**发给模型**） |
| `output` | 出参 JSON Schema（**不发模型**，浅校验与展示；可留空） |
| `config_fields` | 配置项声明（API key、阈值之类） |
| `timeout_ms` | 默认 60000；按预估耗时留 2–3 倍余量 |
| `max_output_bytes` | 默认 65536；返回大表时调大，但别超过模型能读的量 |
| `egress` | 确实要访问外部 HTTP 才开，并在说明里写清目标域名 |

### description 怎么写

模型靠它决定是否调用。写「用途 + 边界」：

- 好：`按区域与月份汇总销售额，返回总额与环比。数据在租户内置表里，不访问外部网络。`
- 差：`一个强大的销售汇总工具`

### parameters 怎么设计

1. **只放模型能可靠给出的东西**：它看不到你的数据库，别设计 `table_id`、`file_path` 这类它猜不出的参数；
2. 每个参数写 `description`；能给 `enum` 就给，能给 `default` 就给默认值；
3. 必填放 `required`；可空的必须有默认值或明确说明「不传则…」；
4. 类型用 `string` / `integer` / `number` / `boolean` / `array` / `object`，不要用 `oneOf` 之类组合（模型容易填错）；
5. **不要设计路径参数**：路径语义不稳定。

```json
{
  "type": "object",
  "properties": {
    "month": { "type": "string", "description": "月份，格式 YYYY-MM，例如 2026-09" },
    "region": { "type": "string", "enum": ["华东", "华北", "华南"], "description": "销售区域；不传则汇总全部" },
    "top_n": { "type": "integer", "default": 5, "description": "返回前 N 个商品，默认 5" }
  },
  "required": ["month"]
}
```

### output 怎么写（可选，建议给）

- 只有设置了 `output` 才校验；校验是**浅校验**：顶层 `required` 键存在 + `properties` 里声明的基础类型匹配（多返回字段不报错）；
- **一旦设置 `output`，脚本输出必须是 JSON 对象**（数组会被拒）；
- 不递归校验深层结构，别指望它替代脚本内校验。

```json
{
  "type": "object",
  "properties": { "total": { "type": "number" }, "items": { "type": "array" } },
  "required": ["total"]
}
```

### config_fields 与配置值

需要租户自己填的东西（API key、接口地址、阈值）走配置，不要硬编码：

| 字段 | 说明 |
|---|---|
| `key` | 脚本里用 `config["<key>"]` 取 |
| `label` / `help` | 填表的人看 |
| `type` | `string` / `int` / `bool` |
| `secret` | 打码回显；**输出里出现该值会被自动遮蔽** |
| `required` | 缺了在**执行前**就报错并指出缺哪个 key |
| `default` | 可空 |

不要把「可选优化项」标成 `required` —— 那会让工具在没填时直接不可用。

### 和全局变量 / 环境变量的边界

**自定义工具拿不到这两者**，别在脚本里按它们设计：

| | 用在哪 | 工具里能拿到吗 |
|---|---|---|
| 全局变量 | 工作流节点输入、`AGENT.md` / `SKILL.md` 的 `${global_vars.X}` | ❌ 工具没有节点输入，文档也不会被展开 |
| 环境变量（`flows.env_vars`） | 只有它所属的那个流程 | ❌ 不是工具的运行上下文 |

工具是沙箱里的一个进程，输入只有 `stdin` 的 `args` + `config`。所以本租户的常量
（API 地址、区域、账号、阈值）**只有一条路**：声明成 `config_fields`，值由人在
「工具 → 自定义工具」里填，脚本从 `config["<key>"]` 取。要让同一份逻辑服务多个租户，
就各建一份工具、各自填配置，不要指望它跟着全局变量走。

也别想用 `args.env` 注入环境变量：`exec` 与 `run_skill_script` 会读 `args.env`，
**自定义工具不会**（执行器只带运行时自有的附加环境变量）。脚本里能读到的进程环境
随沙箱模式（`none` / `nsjail` / `docker`）而变，拿它当配置来源等于把工具绑死在某种部署上。

---

## 第 2 段：实现代码

### 协议（固定）

```
stdin  : {"args": {...模型传的参数...}, "config": {...工具配置...}}
stdout : 一段 JSON（结果本体）
stderr : 日志 / 报错（不要写进 stdout）
```

读完 stdin 到 EOF 才开始执行，不要等交互输入。**stdout 只能有 JSON**。

### 骨架

Python（默认）：

```python
import json
import sys


def main():
    payload = json.load(sys.stdin)          # 读完即 EOF
    args = payload.get("args") or {}
    config = payload.get("config") or {}

    month = str(args.get("month") or "").strip()
    if not month:
        print("参数 month 必填（格式 YYYY-MM）", file=sys.stderr)
        sys.exit(2)                          # 非 0 退出码 → 工具报错，stderr 原文带上

    top_n = int(args.get("top_n") or 5)
    # ... 业务逻辑 ...
    result = {"total": 0, "items": []}
    json.dump(result, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
```

JavaScript（确认环境 `langs` 里有 `js`）：

```js
let raw = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', (c) => { raw += c })
process.stdin.on('end', () => {
  const { args = {}, config = {} } = JSON.parse(raw || '{}')
  if (!args.month) {
    console.error('参数 month 必填（格式 YYYY-MM）')
    process.exit(2)
  }
  process.stdout.write(JSON.stringify({ total: 0, items: [] }))
})
```

Lua（确认环境 `langs` 里有 `lua`；没有标准库 JSON，按运行环境决定是否自带库）：

```lua
local input = io.read("a")               -- 读完整段 stdin
-- 用运行环境提供的 JSON 库解析 input 得到 args / config
local out = { total = 0, items = {} }
io.write(require("json").encode(out))    -- stdout 只写结果
```

Shell（只适合简单命令；复杂逻辑换 python）：

```sh
#!/bin/sh
payload=$(cat)
echo "$payload" >&2                      # 调试信息走 stderr
printf '{"ok": true}\n'
```

### 错误处理约定

| 情况 | 怎么做 |
|---|---|
| 参数不合法 | `stderr` 写一句人话（哪个参数、期望什么），`exit 2` |
| 业务上的「没查到 / 无权限」 | **不要**抛异常，正常返回 `{"ok": false, "error": "该区域无数据"}` |
| 真的异常 | `stderr` 打堆栈摘要 + 非 0 退出码（模型会看到 stderr 原文） |
| 结果太大 | 只返回摘要或前 N 条；超上限直接失败，不会帮你截断 |
| 接口挂了 | 有限重试（1–2 次），仍失败返回 `{"ok": false, "error": "..."}` 并说明可重试 |

关键：**预期内的失败用 JSON 表达，意料外的崩溃用退出码表达**。

### 可以依赖 / 不要依赖

- ✅ `/tool`（本工具目录，只读）：主文件可以 `import` 同目录模块（Lua 的 `require` 要先改 `package.path`，见「引用同目录文件」）；
- ✅ `/workspace`（cwd）可以放**本次调用**的临时文件；
- ❌ 不要指望 `/workspace` 跨调用保留：它随本次运行的工作区走，不保证下次还在；
- ❌ 不要假设能 `pip install` / `npm install`：依赖要么内置，要么用标准库手写；
- ❌ 不要读 `/agent`、`skills/` 等其它实体目录：脚本只挂 `/workspace` 与 `/tool`；
- ❌ 不要往 stdout 打日志：一句 `print("debug")` 就让整次调用失败。

### 多文件怎么组织

```
tools/sum-sales/
├── main.py          ← entry
└── rules.py         ← main.py 可以 import rules
```

- 管理端表单只提交**元数据**（写进 `TOOL.json`）；代码与附属文件在「文件」面板里按目录编辑；
- 超级智能体建完工具后，附属文件用 `write_file` 直接补：`write_file("/tools/sum-sales/rules.py", "<代码>")`；
- 不能写**已登记工具目录之外**的路径，也不能写不存在的工具目录（先 `create_custom_tool`）；
- 工具删除只能走 `delete_custom_tool` / 管理端，不能 `delete_file` 整目录。

### 引用同目录文件

入口的启动形态是 `[解释器, /tool/<entry>]`，**cwd 是 `/workspace`** —— 所以「相对脚本文件」
能引用，「相对 cwd」指向的是工作区，不是工具目录：

| 语言 | 写法 | 说明 |
|---|---|---|
| Python | `from rules import calc` | 脚本所在目录在 `sys.path[0]`，直接可 import |
| Node | `require("./rules.js")` | 模块解析相对**引用它的文件**，与 cwd 无关 |
| Lua | 先 `package.path = TOOL_DIR .. "/?.lua;" .. package.path`，再 `require("rules")` | **只写 `require("rules")` 会失败**：默认的 `./?.lua` 相对 cwd（工作区） |
| Shell | `. "$TOOL_DIR/lib.sh"` | `source` 的相对路径同样跟着 cwd |

`TOOL_DIR` 一律从**脚本自身位置**推导，不要硬编码 `/tool`：

```python
from pathlib import Path
TOOL_DIR = Path(__file__).resolve().parent          # Python
```

```javascript
const TOOL_DIR = __dirname                          // Node（CJS；ESM 用 import.meta.dirname）
```

```lua
local TOOL_DIR = (arg and arg[0] or ""):match("^(.*)[/\\]") or "."   -- Lua
```

```sh
TOOL_DIR=$(cd "$(dirname "$0")" && pwd)             # Shell
```

原因：`/tool` 只在 nsjail / docker 下真实存在。`mode: none` 不做隔离，挂载表只翻译 argv 与
cwd —— 代码里写死的 `/tool/...` 在本地调试会「文件不存在」，而线上却正常，这种分叉最难查。

两条附加约束：

- **stdin 只能读一次**：入口读完就是 EOF，被 import 的模块再读只会拿到空串 —— 参数要显式传进去；
- **配置从 stdin 的 `config` 取**，不必为了拿配置去解析 `TOOL.json`（它可读，但没有这个必要）。

---

## 第 3 段：验证清单

1. **登记成功**：`list_custom_tools` 能看到（或管理端「工具 → 自定义工具」里有），状态「已启用」；
2. **平台能力**：没有「平台未开通」提示（`sandbox.exec` 已开通 —— 自定义工具走 `/v1/exec`，
   与 `exec` 工具同档；`sandbox.code` 是工作流池化脚本的闸，与自定义工具无关）；
3. **语言可用**：页顶「可执行语言」包含你选的那个；如果显示「能力未知」，先用 `exec` 确认解释器存在；
4. **授权**：`grant_agent_tool(agent="<目标>", ref="custom:<name>")`。**只创建不授权 = 模型看不见这个工具**；
5. **最小调用**：管理端面板的「试跑」能用最小参数跑一次（跑的是**已保存**的代码，
   表单有未保存改动时按钮会置灰），确认拿到 JSON 结果；
6. **参数校验**：故意少传必填 / 传错类型，确认错误信息说清是哪个参数；
7. **配置**：`required` 配置留空跑一次，确认报错指出缺哪个 key；填上后再跑；
8. **密钥遮蔽**：用了 `secret` 配置时，确认结果里没有明文密钥（会被替换并带 `_masked`）；
9. **出网**：需要联网时，`egress=true` 且平台开通 `net.egress` 后再验证；断网时的错误要可读。

---

## 常见坑（按出现频率）

1. **stdout 混日志** → 「stdout 不是合法 JSON」。调试输出一律 `stderr`。
2. **创建了但模型看不见** → 忘了 `grant_agent_tool ref="custom:<name>"`，或平台没开 `sandbox.exec`。
3. **选了环境没装的语言** → 调用时才发现。看页顶「可执行语言」，先确认再选；默认 python。
4. **退出码非 0 但脚本其实成功了** → 有些库末尾会返回非 0；确保正常路径显式 `exit 0`。
5. **输出超上限** → 返回摘要/分页，别指望截断。
6. **`output` 设了但返回数组** → 必须是 JSON 对象。
7. **把密钥写进结果** → 用 `secret` 配置项，别把 key 拼进返回值。
8. **改了 entry 没写文件** → 调用直接失败，错误信息像「目录不可达」。改 `entry` 时同一次把代码写好。
9. **依赖装不上** → 沙箱里没有包管理器，换标准库实现。
10. **想改名** → 名称创建后不可改（改名会打断已有的 `custom:<name>` 授权）。要换名就新建 + 重新授权 + 删旧的。
