# 工作流编写指南

## 用途

把一个**固定顺序、要重复跑**的流程（LLM / 脚本 / HTTP / MCP / RPC 串起来）落成租户级工作流，
需要时把它对外开放成对外的 **mcp / rpc 端点**。

本技能负责：判断该不该建成流程、说清工具能建出什么形态、每种节点的 `config` 长什么样、
怎么把上一步的结果传给下一步、怎么对外开放与授权端点。

## 两条落地路径

| 角色 | 怎么做 | 能拿到什么 |
|---|---|---|
| 超级智能体 | `create_flow` → `run_flow` 试跑 →（可选）`grant_agent_tool` | 线性流程一步到位，跑通再对外开放 |
| 人 | 「工作流 → 新建」在画布上编排 | 全部节点类型（分支 / 循环 / 子流程） |

十四个元工具（分类 `flow-admin`，仅超级智能体可用）。**建复杂流程 = 这几把工具配合**：

| 工具 | 作用 |
|---|---|
| `create_flow` | 建实体 + 组装「开始 → 各步」的主干（含 `branches` 分支子链、`children` 容器子链），可声明入口参数（`arguments`）、顺带对外开放 mcp / rpc 端点 |
| `update_flow_arguments` | 增删改入口参数：`set` 按名整体替换、`remove` 删除、`required` 重置必填清单 |
| `list_flows` | 列出本租户全部工作流（含已对外开放的端点协议） |
| `read_flow` | 按 id 或名称取 diagram 原文 |
| `run_flow` | **试运行**：真实跑一遍，回整体状态 + 节点级成败（失败节点带错误码、错误栈与日志） |
| `read_flow_run` | **查运行记录**：按 `run_id` 取节点级输入 / 输出 / 局部变量与错误细节 |
| `delete_flow` | 删定义（连带删端点；**运行记录保留**），回显被删的 `flow_id` + `flow_name` |
| `create_node` | **加一个节点**（`parent_node` 指定容器即可建子节点）；容器自动带 `entry` |
| `insert_node` | 在**一条已存在的边中间插入**节点：删旧边 + 连 A→新→B，原子完成 |
| `update_node` | 改 `label` / `config`（整体替换或 `config_patch` 合并）/ `parent_node`（移进容器） |
| `delete_node` | 删节点，连带删边；容器连整棵子树；被引用时拒绝 |
| `create_edge` | 给已有流程**连一条边**（两个 handle 都必填），可用来接容器子节点、重接主链 |
| `update_edge` | 改一条边的 source / target / handle（按 `edge_id`） |
| `delete_edge` | 删一条边（按 `edge_id`），回显被删的边 |

**`flow` 传 id 还是名称？** 两者等价：UUID 形态按 id 直查，否则按名称查（不区分大小写、忽略首尾空格）。
默认用**名称** —— 字少、不会抄错、省掉一次 `list_flows`；拿到 `flow_id` 之后如果需要钉住同一个流程
（连续操作、怕中途被改名），再用 id。**id 只从工具返回里复制，不要自己拼**：编出来的 id 形态合法，
只会得到一个「不存在」，白花一轮。`run_flow` / `read_flow_run` 的返回体里都带 `flow_id`，直接接上即可。
`read_flow_run` 的 `run_id` 例外：一次运行只有 id，不接受名称。

非超级智能体的 agent 没有这几个工具，只能把编排思路交给用户去前端落地。

## 何时使用

出现这些意图时：

- 「每天 / 每周把 X 拉一遍、汇总、再发出来」——固定顺序且会重复执行
- 「把这个流程做成 MCP 工具，让别的 agent 能调」——建流程 + 对外开放端点
- 「这个流程跑出来的结果不对」——`read_flow` 看结构，前端看运行记录

**先做选型，别默认建流程：**

| 需求形态 | 用什么 | 判据 |
|---|---|---|
| 一次性任务 | 直接做（现有工具 / `exec`） | 只跑一次，不需要复用 |
| 纯函数：模型给参数、返回结构化结果 | 自定义工具（`custom-tool-authoring`） | 无状态、单步 |
| 知识 / 规范 / 话术 | skill（`skill-authoring`） | 没有可执行步骤 |
| 需要容器子节点（循环 / 子流程 / 函数体）、并发组 | **工作流**（工具可建） | `create_flow` 的 `children` 或 `create_node(parent_node=…)`；`entry` 会自动补 |
| 固定顺序 + 多种节点 + 要对外入口 | **工作流** | 本技能 |

## 硬约束（先看，避免白干）

| 约束 | 细节 |
|---|---|
| 名称 | 只能用字母、数字与横线（不以横线开头，≤64），**不能用中文**。它就是端点名 → MCP 的 `tool.name`、agent 侧工具名 `local__<name>`、授权 ref `local:rpc:<name>`，后两者只认 ASCII；中文用途写 `description`。全租户唯一，且**不能与 agent 重名** —— 端点名恒等于实体名，而 `svc_endpoints` 按 `(name, protocol)` 唯一，撞名时端点对外开放直接失败 |
| 描述 | 必填：列表里靠它区分用途，前端编辑器也拒绝空描述 |
| 形态 | 顶层是**线性主链**：`start → 第 1 步 → 第 2 步 → …`；`branch` / `intent` 用 `branches` 拉出子链，`loop` / `process` / `function` 用 `children` 装子链（`entry` 自动补）。这些之外的结构（在链中间插节点、移进容器、改 config、删节点）用 `insert_node` / `update_node` / `delete_node` |
| 支持的节点 | 执行器注册的全部类型都能建：`llm` `code` `http` `mcp` `rpc`（单入单出，直接能跑）、`branch` `intent` `loop` `process` `function` `call` `assign` `memory` `wait` `fork` `result` `progress`、`break` `continue` `return` `exit`。`branch`/`intent` 用 `branches` 建出边（不写就空转）；用户明确要求并发组时，`wait` 的并发组成员（`config.concurrency`）与 `wait.config.fork_node_id` 仍要自己给或后续 `update_node`。`start`（自动生成）、`entry`（容器自动带）、`knowledge`（后端无执行器）**不可创建**；`comment`（纯注释、不连线）与 `layout`（runner 直通）没有执行器但可建 |
| 坐标 | 两种都行：给 `position` 就按你的排（顶层节点要全给，见下「布局」），不给就自动排版（主链从左往右、分支各占一行排在分支节点下方、容器按内容撑开）。位置不影响执行 |
| 并发（默认关闭） | 任何节点都**不要**写 `config.concurrency` —— 工具一律摘掉并回 warning。只有用户**明确要求并发**时才开，并按他的要求二选一：**单体并发** = 一个节点内部并发处理多份数据，写 `{"mode":"mono"}`；**并发组并发** = 多个节点同时跑、由 `wait` 汇合，写 `{"mode":"fork","fork_node_id":"<fork 节点 id>"}`。开启时还要在工具参数里声明形态（`create_flow` / `create_node` / `insert_node` 的 `concurrency=mono\|group`），否则照样被摘掉。`loop` / `process` 容器永远不带这一项。存量流程已写死的并发配置不动（`update_node` 不传 `concurrency` 就保持原样） |

### 布局（坐标可以自己排）

`create_flow` 的每一步都能带 `position: {x, y}`，**顶层节点全给或全不给**：

- 全给 → 完全按你的坐标摆，工具只做重叠检测（冲突写进 `warnings`，不改你的坐标）；
- 少给一个 → 整体退回自动排版，`warnings` 点名缺了谁；
- 容器内节点不用给（坐标相对父容器，工具按内容纵向排好并写 `config.size`）。

审美基线（照着写比自动排版更贴合流程意图）：

| 项 | 要求 |
|---|---|
| 栅格 | `x` / `y` 取 20 的倍数 |
| 主链 | 同一行、`y` 相同、从左往右 |
| 横向间距 | 叶子节点实际宽只有 100–160（`min-width: 100px`，标签很长才接近 300）：主链相邻节点 `x` 步长取 **200**（约留 60–100 空隙）；`branch` 与容器宽 300，其后步长取 **380**。别为"保险"拉到 300 以上，画布会空得很难看 |
| 并发节点 | 带 `config.concurrency` 的节点会多一行「并发控制 + 下拉」，按 **280×64** 留位置（只有用户要求并发时才会出现） |
| 纵向间距 | ≥ 40（叶子高 32；并发节点 64） |
| 分支 | 排在分支节点右下方，每条分支一行，行间 ≥ 72 |
| 换行 | 一行总宽超过 1500 就换行；想控制拐点位置就在该处插一个 `type: "layout"` 步骤 |
| 连线 | 尽量不交叉；冲突时调分支顺序或拉开间距，别靠斜线绕。handle 按两个节点的相对位置选（同一行走左右端点、落差大改垂直端点）—— 具体位置与建议值见 `create_edge` 的参数说明，handle 缺了工具会直接给出该填的值 |

**用 `layout` 节点重复利用高度**：`layout` 是 32px 的圆点，runner 会跳过它并沿第一条出边继续（不影响执行顺序），
所以可以插在主链里当「折行拐点」或「分组锚点」：

```json
[
  {"type": "code", "label": "抓取", "position": {"x": 1200, "y": 60}, "config": {...}},
  {"type": "layout", "label": "折行", "position": {"x": 1560, "y": 60}, "config": {}},
  {"type": "code", "label": "清洗", "position": {"x": 120, "y": 220}, "config": {...}}
]
```

只是视觉拐点，也可以用它把一串节点「框」在一起（放在组的上方/左侧当锚点）。

建完返回的 `nodes` 里带 `position`，不满意就用 `update_node` 的 `position` 单独挪。
| 状态 | 新建即「正常」；废弃只能在管理端切，元工具改不了 |
| 端点 | 只有对外开放过的端点才能被「服务凭证」或 agent 授权引用；未对外开放的流程在外部看不见 |
| 运行记录 | 删除流程**不**级联删运行记录（历史可查） |

## 变量：三个概念别混

1. **`config.inputs`** —— 本节点的入参声明（变量名 → 数据从哪来）
2. **`{{ }}` 模板** —— 在 url / body / messages / script 里引用已解析的入参与节点输出
3. **`config.outputs`** —— 本节点的输出变量声明（脚本类必须声明；其余节点有固定输出）

### `config.inputs` 的写法

```json
"inputs": {
  "question": {"type": "string", "source_type": "argument", "source_key": "question"},
  "prev":     {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "content"},
  "token":    {"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"}
}
```

| source_type | 取自 | 关键字段 |
|---|---|---|
| `argument` | 本次运行的入参 | `source_key` = 参数名，支持 `a.b` / `a[0]` |
| `node` | 前面某个节点的输出 | `source_ref` = **节点 id**；`source_key` = 输出变量名 |
| `env` | 流程环境变量（`flows.env_vars`） | `source_key` = 变量名 |
| `global` | 租户级全局变量（「设置 → 全局变量」） | `source_key` = 变量名 |
| `flow` | 流程变量（运行期可写） | `source_key` = 变量名 |
| `builtin` | 运行上下文（只读） | `session_id` / `run_id` / `user_id` / `schema` / `flow_id` |

取不到值会**直接报错**（节点失败），不会静默给空 —— 排查时先看 `source_key` 有没有拼错。
`global` 同理：变量名不存在、或运行期读不到，都是节点失败，报错里带变量名。

### 模板语法

- `{{ .input.xxx }}` —— 上面 `config.inputs` 解析出来的入参（全局变量也在这里）
- `{{ .output.xxx }}` / `{{ .local.xxx }}` —— 本节点已有的输出 / 局部变量（handler 里用）
- 函数：`json` / `truncate` / `default` / `upper` / `lower` / `trim`

模板里写错字段名**不会报错**，会渲染成 `<no value>` —— 结果里看到它就回来查入参。

**没有 `.global_vars` 这一段上下文**：全局变量只能经输入变量进入（`source_type: "global"`），
模板里一律写 `{{ .input.xxx }}`。早先的 `{{ .global_vars.XXX }}` 已下线 —— 配置里留着它，
流程会在**载入时直接报错**（不会静默渲染成 `<no value>`）。

### 外部配置：环境变量与全局变量

两者都能让配置不落在流程定义里，但**作用域与创建入口不同**，引用方式却是一套（都走 `config.inputs`）：

| | 环境变量 | 全局变量 |
|---|---|---|
| 作用域 | 单个流程（`flows.env_vars`） | 本租户的全部流程 / 智能体 / 技能 |
| 谁建 | 只能人在画布「变量」面板手建，工具建不了，你只能引用已存在的 | 「设置 → 全局变量」，由管理员 / 运营维护 |
| 引用 | `{"source_type": "env", "source_key": "变量名"}` | `{"source_type": "global", "source_key": "变量名"}` |

环境变量的 type 有三种：`string` / `number` / `secret`
（`secret` 编辑器不回显明文，提交空值 = 沿用原值；不随资产包导出；覆盖安装时保留目标租户的值）。
**`type: "global"` 的桥接写法已下线** —— 引用全局变量请直接用 `source_type: "global"`。

全局变量有四个 kind：`text` / `secret` / `number` / `bool`，解析后按原生类型进入 `{{ .input.xxx }}`：

```json
"inputs": {
  "token": {"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"}
},
"headers": [{"key": "Authorization", "value": "Bearer {{ .input.token }}"}]
```

这是**推荐**写法：值不落在流程定义里，资产导出只带引用名，装到别的租户后由对方的变量值填充。
名字拼错会**硬失败**并报出变量名，不会静默空跑。

不知道本租户有哪些变量时，先调 `list_global_vars`（只回名字 / 类型 / 描述，**不回值**），别靠猜。

**两条要注意**：

1. **`llm.messages` 与 `intent.llm.system_prompt` 里引用 `secret` 类全局变量不阻断，但会记告警。**
   值会进模型上下文与运行记录，所以节点日志里会出现「提示词引用了密钥类型的全局变量」。
   密钥优先用于连接 / 工具配置（`http.headers`、`mcp`/`rpc` 的连接参数），别往提示词里塞。
2. **不要把密钥字符串直接写进 config。** 上架时值会被按键名（api_key / token / secret /
   password / credential / authorization）清空，安装方还得重填；跨租户带走就是泄漏。

## 铁律：节点 config 必须写全（工具**不做任何校验**）

`create_flow` 只把 `config` 原样塞进 `data.config`，**一个字段都不校验**。实测：给 `http` / `llm` /
`code` / `mcp` / `rpc` 五种节点各传 `config: {}`，**五次全部返回成功**（`{"steps":1}`）；`read_flow`
回读时这些节点 `data` 里**连 `config` 键都没有**，只剩 `{"label": ...}`。

也就是说：工具会心安理得地建出一个跑不通的节点，错误要等运行时才暴露。所以「建之前就写对」
是唯一可靠的路径。动手前逐条过：

1. **子对象必须有，名字与 `type` 同名**：`type: "http"` → `config.http`，其余同理。缺了它就是空节点。
2. **子对象里的必填项必须有**：见下表，一个都不能省。
3. **`outputs` 要成套声明**（前端建节点时会整套写进 `config.outputs`）。漏声明的后果按节点类型不同：
   `mcp` / `rpc` **没有兜底** —— 漏一个会在写入时报 `output var <名字> not defined`，节点直接失败；
   `http` / `llm` 执行器会补齐固定输出（漏了也能跑）；`code` 漏了则脚本 return 的 key 被丢掉。
4. **`inputs` 哪怕为空也要给 `{}`**，别省这个键。
5. **建完必须 `read_flow` 回读**，逐节点核对 `config` 真的落库、必填项都在。别把 `create_flow`
   返回 `steps: 1` 当成功。

### 必填项与固定输出总表

「固定输出」一列取自本租户**前端实际生成**的节点 config（`read_flow` 可查证）。

| type | 子对象必填 | 子对象可选 | `config.outputs` 应声明 |
|---|---|---|---|
| `http` | `url` | `method`(GET) / `headers` / `params` / `body_type` / `body` / `timeout` / `response_mode` / `on_error` | `body` `mode` `status` `headers` `sse_events` `stream_finished` |
| `llm` | `messages`（非空数组） | `params` / `gateway_model` / `response_mode` | `content` `reasoning_content` `tool_calls` |
| `code` | `script` | `lang`(lua) / `timeout` | **自定义**：脚本 return 的每个 key 都要声明 |
| `mcp` | `connection_id`、`tool_name` | `arguments` / `progress` | `result` `progress` `error` |
| `rpc` | 按 `target_type`：`endpoint` → `endpoint`（**端点名**，不是 id）；`connection` → `connection_id` + `tool_name` | `target_type` / `arguments` / `progress` | `result` `progress` `error` |

三个容易踩的点：

- `headers` / `params` 是 `[{key,value}]` **数组**，不是对象
- `mcp.arguments` 前端存成 **JSON 字符串**（`"{\"a\":6,\"b\":7}"`），不是对象
- `code` 的 `outputs` 没声明过的变量，脚本 return 时会**直接报错**（`脚本返回了未声明的变量`），不是静默丢弃

### 节点参考：只在需要时读

每种节点的完整字段、缺省值、出边规则、常见错误都在单独的文件里，**按需读，别全读**：

```
read_file("/skills/flow-authoring/references/nodes/<type>.md")
```

| 先读什么 | 文件 |
|---|---|
| 节点索引 + 所有节点共用的 `config` 键（`inputs` / `outputs` / `locals` / `targets` / `on_error` / `concurrency`） | `references/nodes/README.md` |
| 连边的形状 / 示例 / 返回体 / 排查（handle 规则以 `create_edge` 的参数说明为准，文档不重复） | `references/edges.md` |
| 加 / 插 / 改 / 删节点（`create_node` `insert_node` `update_node` `delete_node`）、容器与 entry 规则 | `references/nodes-edit.md` |
| 失败重试 / 跳过 / 兜底脚本的形状与优先级 | `references/on-error.md` |
| 执行型节点：`llm` `http` `code` `mcp` `rpc` | `references/nodes/<type>.md` |
| 控制 / 容器：`branch` `intent` `loop` `process` `function` `call` `assign` `memory` `wait` `fork` `result` `progress` | `references/nodes/<type>.md` |
| 控制信号（`break` / `continue` / `return` / `exit`）、结构类（`start` / `entry` / `comment`） | `references/nodes/control-signals.md`、`references/nodes/visual.md` |

上手顺序：先看 `README.md` 的索引（一屏），再只读正在配的那一个节点文件。写线性流程通常只需要
`README.md` + 5 个里的 1~2 个 —— 不要一次把目录读完，那等于把正文又抄一遍。

## 怎么建

```text
create_flow(
  name="daily-sales-report",
  description="每天汇总昨日销售并生成中文简报",
  steps=[
    {"type": "http", "label": "拉数据",
     "config": {
       "http": {"url": "https://api.example.com/sales", "method": "GET",
                "headers": [], "params": [], "body": "", "body_type": "json",
                "timeout": {"enabled": true, "seconds": 30}, "response_mode": ""},
       "inputs": {},
       "outputs": {
         "body": {"type": "string", "description": "响应体"},
         "mode": {"type": "string", "description": "响应模式"},
         "status": {"type": "number", "description": "HTTP 状态码"},
         "headers": {"type": "string", "description": "响应头"},
         "sse_events": {"type": "array[string]", "description": "SSE 事件列表"},
         "stream_finished": {"type": "boolean", "description": "流式传输是否完成"}
       }
     }},
    {"type": "llm", "label": "写简报",
     "config": {
       "inputs": {"data": {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "body"}},
       "llm": {"messages": [{"role": "user", "content": "根据数据写 3 条要点：{{ .input.data }}"}]}
     }}
  ],
  external_protocols=["mcp"])
```

返回 `{flow_id, flow_name, steps, nodes, endpoints}`；`nodes` 是节点清单 `[{id, type, label}]`，用来核对
`source_ref`、分支走向、`call` 的 target。

容器要在里面跑东西，就在该步上加 `children`（仅 `loop` / `process` / `function`，可继续嵌套）：

```text
{"type": "loop", "label": "重试三次",
 "config": {"loop": {"type": "count", "total_times": 3}},
 "children": [
   {"type": "http", "label": "再拉一次", "config": {...}},
   {"type": "break", "label": "拿到就跳出",
    "config": {"condition": "return input.status == 200"}}
 ]}
```

工具会在这个容器里自动建 `entry` 并把 `children` 的首节点接上去 —— 少了 `entry`，容器会
「成功但什么都没跑」，而且不报错。

**节点 id 由工具生成**：`1` 是开始节点，之后按建图顺序递增（先父节点，再按 `branches` 声明顺序
建子链）。纯线性图里第一步是 `2`、第二步是 `3`……**分支会让 id 不再等于步序**，所以：

- `config.inputs` 里的 `source_ref`、`call` 的 `config.target`、`wait` 的 `config.fork_node_id`
  都可以**直接写节点 label**（如 `"拉数据"`），工具会换成真实 id；
- 拿不准就新建后 `read_flow` 回读 diagram，id 一定以返回的 `nodes` / diagram 为准。

顺序建议：

1. `list_flows` —— 确认没有重名、也不是已经建过
2. `create_flow` —— 一次给全 `steps`；用户提到要传入参数时用 `arguments` 声明入口参数；`external_protocols` **只有在用户明确要求对外开放时才传**
3. `read_flow` —— 回读确认结构：线性图是「节点数 = 步数 + 1、边数 = 步数」；含 `branches` 时
   每个子分支各多一条带 `source<childId>` 的边，按 `nodes` 核对 id
4. `run_flow` —— **试跑一次**，看到 `status: success` 再往下走（见下节）
5. 要给 agent 用：`grant_agent_tool(agent="<目标>", ref="local:rpc:<流程名>")`

### 条件分支：`branch` / `intent` 用 `branches`

分支节点**每个子分支都要有一条自己的出边**（执行器按 `source<子分支 id>` 前缀找路），所以它
不能只是主链上的一环。工具侧的写法是给这一步加 `branches`：

```json
{
  "type": "code", "label": "计算5*9",
  "config": {"code": {"lang": "lua", "script": "return { result = 45 }"},
             "outputs": {"result": {"type": "number"}}}
},
{
  "type": "branch", "label": "判断是否等于45",
  "config": {
    "inputs": {"result": {"type": "number", "source_type": "node", "source_key": "result", "source_ref": "计算5*9"}},
    "children": [
      {"id": "c1", "label": "等于45",   "condition": "return input.result == 45",  "exclusive": true},
      {"id": "c2", "label": "不等于45", "condition": "return input.result ~= 45",  "exclusive": true}
    ]
  },
  "branches": [
    {"key": "c1", "steps": [{"type": "result", "label": "输出正确", "config": {"code": {"lang": "template", "script": "正确"}}}]},
    {"key": "c2", "steps": [{"type": "result", "label": "输出错误", "config": {"code": {"lang": "template", "script": "错误"}}}]}
  ]
}
```

规则：

- `branches[].key` 必须是 `config.children[].id`；**无条件兜底**用 `key: "default"`（可以不写，
  不写就没有默认分支，条件全不命中时节点正常结束、什么都不跑）。
- 每条子链会生成一条边：普通分支 `source<key>right`，默认分支 `source<分支节点自己的id>right`。
- **声明了 `branches` 的步骤必须是所在链的最后一步** —— branch 执行完只走 default 边，分支后面
  接主链没有语义；需要「分支后继续」就把后续步骤写进各子链里。
- `children` 里有、`branches` 里漏了的子分支：命中后会被跳过，工具会在 `warnings` 里点名。
- 条件脚本要用 `return` 返回值（`return input.result == 45`），不是裸表达式。

### 连边 / 重接：`create_edge` / `update_edge` / `delete_edge`

`branches` 只能表达「分支节点的子链」。其它结构 —— 容器内的子节点、`fork`/`wait` 并发组、
以及任何「把这条边改接到别处」—— 用这三个工具：

```json
{"flow": "daily-sales-report", "source": "2", "target": "3",
 "source_handle": "source2right", "target_handle": "target3left"}
```

- **规则以工具参数说明为准**：handle 形状、各类型可用位置、硬拦项、按位置的建议都在
  `create_edge` / `update_edge` 的说明里；本文与 `references/edges.md` 不再重复枚举，避免两处说法不一致。
- handle 拿不准**直接调**：缺了会按两个节点的相对位置把该填的值写进报错；组合偏了会回
  `suggested_edge`（边已落库，照它 `update_edge` 即可少绕线）。
- `update_edge` 改 `source` 要同时给 `source_handle`，改 `target` 要同时给 `target_handle`
  （handle 里嵌的是节点 id）；`delete_edge` 只需 `edge_id`。
- 改完再 `read_flow` 回读一次：边工具只动 `edges`，不建节点、不改 `config`。

### 加 / 插 / 改 / 删节点：`create_node` `insert_node` `update_node` `delete_node`

`create_flow` 一次给全 `steps` 最省事；漏了节点、想插到链中间、要改 config、要挪进容器，用这四个。

```json
// 加一个节点（建在不传 parent_node = 顶层）
{"flow": "daily-sales-report", "type": "code", "label": "清洗", "config": {"code": {"lang": "lua", "script": "..."}}}

// 加进容器：loop / process / function 的 id 填 parent_node
{"flow": "daily-sales-report", "type": "http", "parent_node": "7"}

// 在已有边中间插一个节点（原子：删旧边 + 连 A→新→B）
{"flow": "daily-sales-report", "edge_id": "04958", "type": "code", "label": "审计"}

// 只改一项 config（顶层合并，值为 null 表示删掉该键）
{"flow": "daily-sales-report", "node_id": "3", "config_patch": {"on_error": {"action": "retry"}}}

// 移进容器：横跨新旧容器的边会被删除（跨父节点不合法），返回 removed_edges
{"flow": "daily-sales-report", "node_id": "4", "parent_node": "7"}

// 删节点（连带删边；容器连整棵子树）
{"flow": "daily-sales-report", "node_id": "4"}
```

要点：

- **容器一定要有 `entry`**：`create_node` / `create_flow` 都会自动补；手写 diagram 或从别处拷来的图没有 entry，
  容器会「成功但什么都没跑」，且不报错。
- **节点 label 全图唯一**：`source_ref` / `call.target` / `fork_node_id` 都能按 label 写，重名会指错节点。
  显式写重名直接报错；`label` 留空则由工具自动加 `-2` 后缀（`entry` 也参与，第二个容器叫 `入口-2`）。
- **`config` 是整体替换，`config_patch` 是顶层合并**，二选一。改完返回体里带最终 `config`，核对一眼。
- **`function` 只能建在顶层，`return` 只能建在 `function` 里**；`insert_node` 只能插「既有入边又有出边」的节点。
- **删节点会被引用拦住**：`config.inputs.*.source_ref`、`config.target`（call）、`config.fork_node_id`（wait）
  指向它时直接拒绝并列出来源，先 `update_node` 改引用再删。
- 完整规则见 `references/nodes-edit.md`。

## 建完先试跑（`run_flow`）

```json
{"flow": "daily-sales-report", "inputs": {"q": "昨天"}}
```

返回形状：

```json
{
  "flow_name": "daily-sales-report",
  "status": "success",              // success / failed / cancelled
  "result": "最终输出文本",           // result 节点 / return 收集到的内容
  "duration_ms": 3210,
  "err_code": "timeout",            // 失败时才有：取最早失败节点的错误码
  "run_id": "0190f...",             // 后续查细节用 read_flow_run
  "nodes": [                        // 按执行顺序，成功节点也在
    {"node_id": "2", "type": "http", "status": "success", "duration_ms": 1200},
    {"node_id": "3", "type": "llm", "status": "failed", "err_code": "timeout",
     "err_msg": "...", "err_stack": "（traceback，失败节点才有）", "logs": "（日志片段）"}
  ]
}
```

四条必须知道的事实：

- **这是真实执行**：http / rpc / mcp 会真的发请求，llm 会真的计费，code / handler 会真的跑脚本。
  有副作用、有费用 —— 只在需要验证流程跑得通时跑，别拿它试探参数。
- **单次调用受工具超时约束（缺省 60 秒）**：长流程会在超时点被取消，返回 `status: cancelled`。
  此时 `nodes` 停在中断点，后面的节点一个都没跑 —— 别把它读成「流程逻辑坏了」。
- **子流程读哪一份由 `follow_drafts` 决定**（缺省 `true`）：`true` 表示它调起的子流程也读草稿
  （改 A、改 B 一起看效果，不必先把 B 发布一遍）；`false` 表示子流程按**已发布的正式版本**跑，
  用来验证线上那一套链路 —— 子流程从未发布过会直接报错，不会悄悄退回草稿。主流程本身永远跑当前草稿。
- **不回节点输出变量**（可能几十 KB）：`result` 是最终输出，中间数据用 `read_flow_run` 按节点取。
  失败时先看最早那个 `failed` 节点的 `err_code` / `err_msg` / `err_stack` / `logs`。

## 失败怎么查（`read_flow_run`）

`run_flow` 只回答「成没成、卡在哪」，要问「为什么」用它按 `run_id` 取全量记录：

```json
{"run_id": "0190f...", "only_failed": true}
```

- 缺省只回**失败节点**（`only_failed: false` 才回全链路）；`nodes: ["3","4"]` 可只取指定几步。
- 每个节点带 `input_vars` / `output_vars` / `local_vars` / `extra`（节点类型特有结果，如 HTTP 状态、
  token 用量），以及 `err_code` / `err_msg` / `err_stack` / `logs` / `retry_count` / `error_action`。
- 变量超限时退化成 `{"_truncated": true, "_bytes": 12345, "keys": [...]}` —— 说明「键都在、值太大」，
  不是数据丢了；落库本身按 64KB 顶层 key 截断，节点上的 `truncated: true` 表示落库时就截过。
- `run_id` 也可以来自前端「运行记录」列表：**前端 debug 跑出来、别的 agent 调起来的记录都能查**。

排障顺序：`run_flow` 拿到 `run_id` 与最先失败的节点 → `read_flow_run` 看该节点的
`input_vars`（上游给的值对不对）与 `output_vars`（吐出的值对不对）→ 对照节点参考文件改 `config` →
`read_flow` 确认改动落地 → 再 `run_flow`。`inputs` 的键要与流程声明的 `arguments` 同名；
没声明过也能传，但不会出现在任何 `source_type=argument` 的变量里。

## 端点：对外开放与授权

> **对外开放端点 = 对外暴露可调用接口，属于有副作用的动作。未经用户明确要求，`create_flow` 一律不传
> `external_protocols`。** 用户说「能访问百度、输出 html」是功能描述，不是对外开放授权。要对外开放先问清：
> 哪个流程、mcp 还是 rpc、端点名、谁能调。已误对外开放的立刻 `delete_flow` + 重建撤销，并如实告知 ID 变了。

- **对外开放**：`external_protocols=["mcp"]` / `["rpc"]` / 两者都给；不对外开放 = 没有对外入口
- **端点名 = 流程名**：流程改名后端点名跟着变，旧的 `local:rpc:<旧名>` 授权会**静默失效**
- **确认对外开放成功**：`list_flows` 的 `endpoints` 里能看到协议名；没有就是没发出去
- **谁能用**：
  - agent → `grant_agent_tool(agent, ref="local:rpc:<流程名>")`（MCP 侧工具名是 `local__<流程名>`）
  - 外部系统 → 「服务凭证」只能授权**已对外开放**的端点，未对外开放的流程那边根本看不到
- **入参 schema**：端点对外的参数说明取自 `flows.arguments` —— `create_flow` 的 `arguments` 就是它，
  后续增删改用 `update_flow_arguments`。没声明过时端点仍能收任意参数，但客户端拿不到任何参数说明，
  所以正式对外开放前先声明好；改完记得**发布**（正式入口读的是已发布版本）。

## 维护

| 要改什么 | 怎么做 |
|---|---|
| 入口参数 | `update_flow_arguments`（`set` 增改 / `remove` 删 / `required` 重置必填） |
| 名字 / 描述 / 端点 | 没有改名元工具：`delete_flow` + `create_flow` 重建（会换掉 flow id：服务凭证白名单按目标 id 授权，旧行会静默失效） |
| 加节点（含容器子节点 / 循环 / 子流程） | `create_node`（`parent_node` 指定容器）；或 `create_flow` 的 `children` |
| 在链中间插节点 | `insert_node(edge_id=…)`，原子完成，不用手删手连 |
| 改节点 config / 移进容器 | `update_node(node_id=…, config_patch=… / parent_node=…)` |
| 删节点 | `delete_node(node_id=…)`；容器连整棵子树一起删 |
| 连边 / 改接 / 删边 | `create_edge` / `update_edge` / `delete_edge`（只动 edges） |
| 状态（正常 / 废弃） | 管理端；废弃后不可改定义、不可对外开放 |
| 删除 | `delete_flow(flow=<id 或名称>)`；端点一并删除，运行记录保留 |
| 查看结构 | `read_flow` 给 diagram 原文；概览用 `list_flows` |

## 排查

| 现象 | 多半是 |
|---|---|
| `read_flow` 里节点只有 `{"label"}`、没有 `config` | 传了 `config: {}` —— `create_flow` 不校验，空对象被原样丢掉，节点是废的 |
| 创建成功，但返回的 `warnings` 说某步空转 | `branch`/`intent` 没写 `branches`（或漏了某个子分支）→ 补 `branches`；`loop`/`process`/`function` 没有子节点 → 用 `children` 或 `create_node(parent_node=…)` 补；`wait` 没有并发组成员 → 用户要求并发组时用 `update_node(concurrency=group)` 补 `config.concurrency` |
| 运行报 `body entry node not found` | 容器缺 `entry` 子节点。`loop` / `process` / `function` 都会报这个 `config` 错误（以前是静默成功跳过，整个子流程没执行却看不出来）：`create_node` / `create_flow` 会自动补，手写 diagram 不会 |
| 报「label … 必须唯一」 | 同名的节点已存在（label 是 `source_ref` / `call.target` 的写法，重名会指错节点）：换一个，或把 `label` 留空让工具自动加 `-2` 后缀 |
| 分支命中后什么都没发生 | `children` 的 `id` 与 `branches` 的 `key` 不匹配（工具会拦），或 `condition` 忘了 `return` |
| 运行报「分支 / 意图节点没有配置任何分支」 | `branches` **只建出边**，不写 `config.children` —— 候选分支 / 意图仍要自己在 `config` 里写全 |
| 运行报 `output var xxx not defined` | `mcp` / `rpc` 节点的 `config.outputs` 没声明这个输出（这两个节点不兜底；`http` / `llm` 会补齐） |
| 第一步就报 config 相关 | 少了该节点必需的子对象：`llm.messages` / `http.url` / `code.script` / `mcp.connection_id` + `tool_name` |
| 结果里出现 `<no value>` | 模板字段名写错，或 `config.inputs` 没声明这个变量 |
| 报「node result not found」 | `source_ref` 指向了不存在 / 还没执行的节点（id 从 `1` 开始，第一步是 `2`） |
| `create_edge` 报「target_handle 必填」 | 两个 handle 都必须给全，工具不补缺省值；报错里已按两个节点的位置给出建议值，照抄即可 |
| `create_edge` 报「只能有一条入边」 | 该入口位置已有边；要换来源用 `update_edge`，或换另一个入口位置（旧边会被顶掉） |
| `create_edge` 报「跨父节点」 | 两个节点的 `parent_node` 不同 —— 容器子节点只能和同容器内的节点相连 |
| 端点调用提示找不到 / 未授权 | 端点没对外开放，或 ref 用了改名前的旧流程名 |
| 改了代码但行为没变 | 改的是前端未保存的草稿，还是库里那份？以 `read_flow` 回读为准 |
| 报「平台未给本租户开通能力」 | code 节点要走沙箱脚本执行，找平台管理员开通，重试没用 |
