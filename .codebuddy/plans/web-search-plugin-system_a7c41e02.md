# 插件体系：磁盘 Python 插件 + 自注册（首个工具 `web_search`）

**状态**：P0–P3 已落地并跑通测试；P4（治理：限额 / 审计列 / 密钥加密）待做
**日期**：2026-10-20
**范围**：`{root_dir}/plugins` 下的 Python 插件宿主（`pluginhost`）与 SDK；`nexus` 侧的发现 / 授权目录 / 执行通道 / 管理端；首个落地工具 `web_search`（实现：tavily / bing_rss / searxng）

---

## 1. 定位（一切细节由它推出）

> **插件是磁盘上的 Python 代码，自己注册工具；nexus 只做「发现 + 租户配置 + 调度」。**

一句话分工：**目录即种类，`tool.py` 定工具面，子目录定实现，`plugins` 表定本租户用哪家。**

三个核心决策：

1. **自注册**：没有中心清单文件。插件 import 时用装饰器登记工具面（`@tool`）与实现（`@impl`），宿主 `list` 时汇总成 manifest。
2. **工具面收窄**：一个工具只暴露一个 LLM 名字（`web_search`），多种实现（tavily / bing_rss / searxng）藏在后面，由租户配置选主实现 + 兜底链。换后端对模型、技能、`AGENTS.md` 零改动。
3. **每次调用一个子进程**：`pluginhost invoke` 起独立 Python 进程，stdin 一次 JSON、stdout 一行 JSON。不做常驻宿主（搜索本身是几百毫秒网络调用，50–80ms 启动可忽略），不做进程池（崩溃天然隔离）。

---

## 2. 已锁定的决策（不要再回头讨论）

| 决策 | 取值 | 直接后果 |
|---|---|---|
| 插件目录 | `{root_dir}/plugins`（`agent.root_dir` 下） | 不在 `tenants/` 子树内 → `Context.StoragePath` 的租户前缀校验天然拒绝，agent 文件工具够不着；nexus 也不把它挂进沙箱 |
| 配置作用域 | 租户级（DB `plugins` 表） | 同份磁盘插件，各租户各配各启；未启用的租户在目录里 `available=false` |
| 密钥 | 明文 `jsonb` | 与 `llm_providers.api_key` / `mcp_connections.auth` 一致；管理端对 `secret:true` 字段回显打码 |
| 插件种类 | `plugins/<kind>/<tool>/`，当前只支持 `kind=tools` | 未知 kind 只记录不加载；将来加类不改已有插件与授权 |
| 工具面 | 只暴露一个 `web_search` | 授权以工具为单位（`plugin:web_search`），与厂商无关 |
| 命名 | `web_search`（snake_case） | 工具名 = 目录名 = ref subject，三者同一字符串 |
| 进程模型 | 每次调用一个子进程 | 超时 kill 进程组；无状态；不并发串行化 |

---

## 3. 目录结构与自注册

```
{root_dir}/plugins/
  _host/pluginhost.py            # 唯一入口：list / invoke
  _sdk/flame_plugin/__init__.py  # @tool / @impl / @field / ctx / PluginError
  tools/                         # kind：工具类插件（当前唯一支持）
    web_search/
      tool.py                    # 工具面（唯一一份 schema）
      tavily/plugin.py           # 实现
      bing_rss/plugin.py
      searxng/plugin.py
    web_fetch/                   # 第二个工具照此新增，nexus 零改动
      tool.py
      jina/plugin.py
```

规则：

- 扫描 `plugins/<kind>/<tool>/`；`_` 开头的目录整棵跳过。
- `tool.py` 声明工具面（`@tool`），实现放子目录的 `plugin.py`（`@impl` + `@field`）。
- **单个实现 import 失败只让该实现缺席**，不影响同一工具下其它实现，也不影响其它工具。
- 宿主 import 插件前把 `sys.stdout` 重定向到 stderr，只在最后写一行结果 —— 插件作者的 `print()` 不会污染协议。
- 实现依赖由部署方 `pip install -r requirements.txt`；缺依赖表现为该实现的 error。

### 3.1 工具面声明

```python
# plugins/tools/web_search/tool.py
# 注意：tool(...) 是**直接调用**（不接函数）—— 它只往模块 globals 里写声明。
from flame_plugin import tool

tool(
    name="web_search",
    display_name="网络搜索",
    description="搜索网络并返回标题/链接/摘要。适合需要时效性的事实查询；"
                "同一问题换关键词多搜两次比一次搜很多条更有效。",
    parameters={
        "type": "object",
        "properties": {
            "query":   {"type": "string", "description": "检索词"},
            "count":   {"type": "integer", "minimum": 1, "maximum": 20, "description": "条数，默认 5"},
            "recency": {"type": "string", "enum": ["any", "day", "week", "month", "year"]},
            "site":    {"type": "string", "description": "限定站点，如 example.com"},
            "lang":    {"type": "string", "description": "语言，如 zh / en"},
            "include_answer": {"type": "boolean", "description": "是否要一段生成式摘要（实现支持时返回）"},
        },
        "required": ["query"],
    },
)
```

### 3.2 实现声明

```python
# plugins/tools/web_search/tavily/plugin.py
from flame_plugin import impl, field, PluginError

@impl(name="tavily", display_name="Tavily")
@field("api_key", label="API Key", secret=True, required=True)
@field("max_results", label="默认条数", type="int", default=5)
def invoke(ctx, args):
    r = ctx.http.post("https://api.tavily.com/search", json={
        "api_key": ctx.config("api_key"),
        "query": args["query"],
        "max_results": args.get("count") or ctx.config("max_results") or 5,
    })
    return {
        "results": [{"title": x["title"], "url": x["url"], "snippet": x["content"]} for x in r["results"]],
        "answer": r.get("answer"),
    }
```

实现签名统一 `invoke(ctx, args: dict) -> dict`（不用具名参数，避免 schema 与签名漂移）。`ctx` 提供 `config(key)` 与 `http`（统一超时/重试/错误分类）。错误用 `raise PluginError(code="auth"|"quota"|"network"|"config"|"other", message="...")`。

---

## 4. 宿主协议（`pluginhost.py`）

```bash
python _host/pluginhost.py list   --dir {root_dir}/plugins
python _host/pluginhost.py invoke --dir {root_dir}/plugins --tool web_search --impl tavily
```

**list 输出**：

```json
{"tools":[{
  "kind":"tools","name":"web_search","display_name":"网络搜索","version":"0.1.0",
  "description":"…","parameters":{ },
  "implementations":[
    {"name":"tavily","display_name":"Tavily","config_fields":[{"key":"api_key","label":"API Key","secret":true,"required":true,"type":"string"}]},
    {"name":"bing_rss","display_name":"Bing RSS","config_fields":[]},
    {"name":"searxng","display_name":"SearXNG","config_fields":[{"key":"endpoint","label":"服务地址","required":true,"type":"string"}]}
  ]
}],
"errors":[{"path":"tools/web_search/bing_rss","error":"ImportError: no module named feedparser"}]}
```

**invoke stdin**：

```json
{"meta":{"tenant":"t0","agent":"main","run_id":"..."},"config":{"api_key":"..."},"args":{"query":"..."}}
```

**invoke stdout（恒为一行）**：

```json
{"ok":true,"result":{"results":[...],"answer":null}}
{"ok":false,"error":{"code":"auth","message":"401 Unauthorized"}}
```

`meta` 只用于日志与排障，插件不得依赖它做鉴权。

---

## 5. 工具面与命名规则

| 项 | 取值 |
|---|---|
| LLM 工具名 | `web_search`（无前缀，与内置工具同一层） |
| 授权 ref | `plugin:web_search`（组 = 工具；**不接受第三段**） |
| 展示 | `Source = "plugin:web_search"`，`Kind = "plugin"`，`SupervisorOnly = false` |
| 重名 | 与内置工具（`tools.List()`）重名 → 该工具标 error 并跳过，管理端红字要求改名 |
| 缺省授权 | **不参与 main 的「缺省全开」**，必须显式授权（涉及出网与计费） |

`ToolNameOfRef("plugin:web_search")` → `web_search`；`ParsedRef.IsGroup(plugin)` 恒 `false`（插件工具只有单工具 ref，与 `local:rpc:weather → local__weather` 同类的「单段名」处理）。

---

## 6. nexus 侧改动清单

### 6.1 新增

| 文件 | 职责 |
|---|---|
| `nexus/service/plugin/manifest.go` | `Manifest{Kind,Name,DisplayName,Description,Version,Parameters,Implementations,Error}`、`Implementation{Name,DisplayName,ConfigFields}`、`ConfigField` |
| `nexus/service/plugin/host.go` | `list` / `invoke` 的 exec 封装：拼命令、写 stdin、读一行 stdout、stderr 截 4KB 入日志；超时 kill 进程组（Unix `Setpgid`+`kill(-pgid)`，Windows `CREATE_NEW_PROCESS_GROUP`+`taskkill /T`） |
| `nexus/service/plugin/registry.go` | `map[toolName]Manifest` + `discoveredAt` + `errors`；启动拉一次、每 60s 刷新、目录 mtime 变化立即刷新；刷新失败保留上次成功结果（不猜）；重名 / 内置冲突在此判定 |
| `nexus/service/plugin/runner.go` | 实现 `tools.PluginRunner`：读租户 `plugins` 行 → 主实现 → 兜底链逐个调 → 结果 12KB 截断 → 错误映射 |
| `nexus/shared/repository/plugins/` | `repository.go` + `postgres.go`，形状照 `mcpconnections`（Get/List/Upsert/Delete/SetActive） |
| `nexus/admin/plugins/handlers.go` | 见 §9 |
| `nexus/sql/plugins.sql` | 存量租户增量脚本 |

### 6.2 改动

| 文件 | 改动 |
|---|---|
| `nexus/config.yaml` + config 结构体 | 新增 `plugin: {dir, python, timeout: 30, refresh_interval: 60}`；`dir` 为空时取 `{agent.root_dir}/plugins`，不存在则建空目录 + Info 日志 |
| `nexus/service/agent/tools/tool.go` | `Context.Plugins PluginRunner`；包级 `SetPluginRunner`（同 `SetSandbox` / `SetInteractive` 的启动注入惯例）；接口 `{Has(name) bool; Invoke(ctx, schema, tool, args)}` |
| `nexus/service/agent/tools/registry.go` | `ExecuteTool` 顺序改为：**授权白名单闸 → 内置命中 → `Plugins.Has` 命中则委托 → ErrToolNotFound** |
| `nexus/service/agent/toolgrant/ref.go` | `ScopePlugin` 进 `ParseRef` 白名单；`IsGroup` 返回 false；`ToolNameOfRef` 返回 Subject；拒绝第三段 |
| `nexus/service/agent/toolgrant/catalog.go` | `ToolInfo` 加 `Parameters map[string]any`（外部工具没有 `tools.List()` 兜底，必须自带 schema）；`Groups` 产出 `kind=plugin` 组；`ToolsOfRef` / `AllTools` 增插件分支 |
| `nexus/service/agent/agent_impl.go` | `buildToolDefs` 改从 `res.FinalTools`（叶子 ToolInfo，带 schema）声明；`isExternalToolName` 只继续过滤 `mcp__/rpc__/local__`，不再误伤插件工具 |
| `nexus_desk/src/tenant/config/admin.js` | 新增 `plugins` API 段 |
| 前端 `Capabilities.vue` 或新页 | 「插件」页/标签；工具授权树给 `kind=plugin` 加图标文案（`Agents.vue` 无需改结构） |

### 6.3 注入时序

`main` 启动：建目录 → `plugin.SetRunner(...)` → `plugin.Refresh()`（失败只记日志，不阻塞启动）→ `tools.SetPluginRunner(runner)` → 起 60s 定时器。首次 `list` 未完成时工具目录为空，60s 内补齐（与 sandboxd capabilities 的处理同口径）。

---

## 7. 运行时语义

- **可用性**：`is_active` && manifest 无 error && 主实现在 manifest 里。未启用 → 目录里 `available=false`（看得见、授不了、调不动）。
- **兜底**：主实现失败 → 依次试 `fallbacks`；`config` 类错误（缺 key / 实现不存在）**不降级**直接报错；全部失败 → 聚合错误，带每家一句话原因。
- **结果**：`{provider, query, results[{title,url,snippet,published?}], answer?, notes[], truncated}`。实现不支持的入参进 `notes`；总字节上限 12KB，超限截断并置 `truncated`。
- **超时**：沿用 `Context.Timeout`；子进程超时 kill 整组，避免 Python 孙进程残留。
- **并发**：每次调用独立子进程，天然并行，无池。
- **日志**：`schema / agent / tool / impl / 耗时 / 错误码`；stderr 截断后随错误一并落日志。

---

## 8. 数据表

```sql
-- nexus/platform/provision/tenant_template.sql 追加（无 schema 前缀）
-- 存量租户走 nexus/sql/plugins.sql
CREATE TABLE plugins (
    id uuid NOT NULL,
    name varchar(64) NOT NULL,            -- 工具名：web_search
    impl varchar(64) NOT NULL,            -- 主实现：tavily
    fallbacks jsonb DEFAULT '[]'::jsonb,  -- ["searxng"]，有序
    configs jsonb DEFAULT '{}'::jsonb,    -- {"tavily":{"api_key":"..."},"searxng":{"endpoint":"..."}}
    is_active boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamptz DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT plugins_pkey PRIMARY KEY (id),
    CONSTRAINT plugins_name_key UNIQUE (name)
);
```

**配置按实现分键存**（`configs`），所以切换主实现不会洗掉另一家的 key。`PUT` 按实现合并，不是整包覆盖 —— 前端对 `secret` 字段回显打码，用户没改就不回传，后端保留旧值。

不建 `plugin_tools` 表：工具清单唯一真源是磁盘 manifest，DB 只记「本租户装了哪个 + 怎么配」。

---

## 9. 管理端

页面形态：**一张卡片 = 一个工具**。

```
网络搜索 (web_search)
  主实现  [ Tavily        ▾ ]
  配置    API Key  sk-••••3f2a
  兜底    [+ 添加] SearXNG ↑↓ ✕
  启用    [ ✓ ]        测试连接
```

API：

```http
GET  /admin/plugins/                  # 磁盘发现 + 本租户状态（含 parameters、implementations、error）
PUT  /admin/plugins/:name             # {impl, fallbacks, configs, is_active}（按实现合并）
POST /admin/plugins/:name/test        # {impl, query} → {ok, results(前3条), error}
POST /admin/plugins/refresh           # 立即重新扫描磁盘
```

「测试连接」跑一次真实调用，回显前 3 条结果 + 原始错误（含 stderr 摘要）。

---

## 10. 阶段与验收

**P0 宿主可跑（不碰 nexus 业务）** ✅ 已完成（2026-10-20）
`_sdk/flame_plugin` + `_host/pluginhost.py` + `tools/web_search/{tool.py, tavily, bing_rss, searxng}` + `host.go` + 单测。
验收：`list` 返回 `web_search` + 3 个实现 + 1 条被隔离掉的 error；`invoke` 拿到结果；超时把进程组杀干净。测试用 `PLUGIN_TEST_PYTHON` 门控，缺 Python 自动 skip。

落地文件：

| 文件 | 说明 |
|---|---|
| `nexus/data/plugins/_sdk/flame_plugin/__init__.py` | `tool()` / `@impl` / `@field` / `Ctx` / `HttpClient`（标准库 urllib，零第三方依赖）/ `PluginError` |
| `nexus/data/plugins/_host/pluginhost.py` | `list` / `invoke`，stdout 只留一行 JSON，插件 print 全落 stderr |
| `nexus/data/plugins/tools/web_search/tool.py` | 工具面（6 个入参，`query` 必填） |
| `nexus/data/plugins/tools/web_search/{searxng,bing_rss,tavily}/plugin.py` | 三个实现，均支持可选 `endpoint` 覆盖（测试与内网代理用） |
| `nexus/service/plugin/{manifest,errors,host,sysproc_unix,sysproc_windows,host_test}.go` | Go 侧宿主与错误码 |

测试（`cd nexus && go test ./service/plugin/`，8 个用例全绿）：list 发现、SearXNG 结果映射 + count 截断、Bing RSS XML 解析 + HTML 剥标签、Tavily 401 → `auth`、缺 key → `config`、坏实现隔离、超时整组杀（700ms 上限，0.78s 返回）、未知工具 → `config`。

两个实现期发现（已修，勿踩）：

1. **Windows 管道编码**：Python 默认按 locale（cp936）编解码 stdin/stdout，中文查询词会被解成 surrogate 再在输出编码时炸掉。宿主 `main()` 开头把三个流 reconfigure 成 UTF-8，Go 侧同时传 `PYTHONUTF8=1` / `PYTHONIOENCODING=utf-8`。
2. **配置字段顺序**：装饰器自下而上执行，`@field` 若直接 append 会得到反序字段；改为 `insert(0)`，且 `@field` 写在 `@impl` 上方也能同步到实现元信息上。

**P1 发现与目录** ✅ 已完成（2026-10-20）
`registry.go` + `plugin:` 配置项 + `plugins` 表/仓储 + `ToolInfo.Parameters` + `ScopePlugin`。
验收：`/admin/toolcatalog` 出现 `plugin:web_search`（带 parameters），可写入 `agent_tool_grants`；停用后 `available=false`。

落地文件：

| 文件 | 说明 |
|---|---|
| `nexus/service/plugin/registry.go` | 清单缓存 + 三条加载期硬规则（kind 白名单 / 不与内置重名 / 跨 kind 全局唯一）+ 目录签名跳过 + 后台刷新 |
| `nexus/shared/repository/plugins/{repository,postgres}.go` | 租户级配置仓储（Get/List/Upsert/SetActive/Delete） |
| `nexus/shared/models/models.go` | `models.Plugin` + `FallbackImpls()` + `ImplConfig(impl)` |
| `nexus/shared/config/config.go` | `PluginConfig{Dir,Python,Timeout,RefreshInterval}` + `DirOrDefault/TimeoutDuration/RefreshIntervalDuration` |
| `nexus/platform/provision/tenant_template.sql` / `nexus/sql/plugins.sql` | `plugins` 表（模板 + 幂等增量脚本） |
| `nexus/service/agent/toolgrant/ref.go` | `ScopePlugin`；`IsGroup=false`；`ToolNameOfRef` 返回 Subject；拒绝第三段 |
| `nexus/service/agent/toolgrant/catalog.go` | `ToolInfo.Parameters`；`pluginTool/pluginTools`；`ToolsOfRef`/`AllTools`/`Groups` 三处接入 |
| `nexus/cmd/main.go` | 启动建目录 → `NewHost` → `NewRegistry(tools.Names())` → 首次 `Refresh` → `SetSource` → 后台 `Start` |

测试（`go test ./service/plugin/ ./service/agent/toolgrant/` 全绿）：registry 发现/内置重名拒绝/跨 kind 重名拒绝/未知 kind 拒绝/目录未变不重扫且失败保留旧结果；toolgrant 的 `plugin:` ref 解析、启用/停用/未知工具三种 `ToolsOfRef` 行为（用假 Source + 假仓储，不起 python 不连 DB）。

实现期新增的坑（已修）：

3. **目录 mtime 不能进签名**：Python import 会生成 `__pycache__`，改掉上层目录的 mtime，导致「目录没变却每轮重扫」。签名只记目录名 + 文件 mtime，并整棵跳过 `__` 前缀目录。
4. **`plugin.Default()` 要能注入假实现**：为了让 toolgrant 的目录测试不依赖 python，注册表改成 `Source` 接口（`Snapshot()/Get()`）+ `SetSource`；生产的 `*Registry` 就是它的一个实现。

**P2 执行通道** ✅ 已完成（2026-10-20）
`runner.go` + `ExecuteTool` 委托 + `agent_impl` 改 `FinalTools`。
验收：授权后会话里真能搜；未授权调用被拒（负向用例必备）；主实现 401 时按配置降级 / 不降级（两条用例）。

落地文件：

| 文件 | 说明 |
|---|---|
| `nexus/service/plugin/runner.go` | `Invoker` 接口（*Host 是生产实现）+ 主实现/兜底链 + 12KB 裁剪 + config 不降级 |
| `nexus/service/agent/tools/tool.go` | `PluginRunner` 接口（`Has`/`Invoke`）+ `SetPluginRunner` + `Context.Plugins`（单测可注入） |
| `nexus/service/agent/tools/registry.go` | `ExecuteTool` 顺序改为 **授权闸 → 内置 → 插件 → not found**；内置执行抽成 `executeBuiltin` |
| `nexus/service/agent/agent_impl.go` | `buildToolDefs` 改用 `ResolveResult.FinalTools`（带 schema）；`isExternalToolName` 只跳 mcp/rpc/local，不再误伤插件 |
| `nexus/cmd/main.go` | `tools.SetPluginRunner(plugin.NewDefaultRunner(host, registry))` |

**顺序为什么重要**：`ExecuteTool` 先把名字过 `AllowedTools` 再去找工具。反过来写的话，一个未授权的插件名字会先被送去执行器——起 python 进程、花掉租户的检索配额，然后才被拒。

测试：runner 7 个用例（主实现、网络错误降级、config 不降级、停用/未配置、未知工具、兜底链里的幽灵实现、12KB 截断）+ tools 3 个分派用例（授权后委托、**未授权在起进程前被挡**、无执行器时仍是 not found）。

**P3 管理端** ✅ 已完成（2026-10-20）
handlers + 插件页（选实现 / 配置 / 兜底链 / 测试连接 / 启停）。
验收：目录里新增一个实现文件 → 60s 内出现在下拉里，切主实现不重启生效。

落地文件：

| 文件 | 说明 |
|---|---|
| `nexus/admin/plugins/handlers.go` | `GET /admin/plugins/`（磁盘清单 + 租户状态 + 扫描错误）、`PUT /:name`（按实现合并保存）、`POST /:name/test`（真跑一次，可带未保存配置）、`POST /refresh` |
| `nexus/admin/admin.go` | `/admin/plugins` 路由组（刻意没有 create —— 插件靠磁盘自注册） |
| `nexus/service/plugin/registry.go` | 新增 `SetDefault` / `Current`（管理端「重扫磁盘」需要具体注册表，`Source` 接口视图不够） |
| `nexus/shared/models/models.go` | `Plugin.AllConfigs()`（按实现分键读配置） |
| `nexus_desk/src/tenant/config/admin.js` | `plugins` API 段 |
| `nexus_desk/src/tenant/components/capabilities/PluginsTab.vue` | 卡片列表 + 配置抽屉（实现下拉 / 动态配置表单 / 有序兜底链 / 测试连接 / 启停） |
| `nexus_desk/src/tenant/components/Capabilities.vue` | 新增第三个 tab「插件」（工具 / 插件 / 端点） |

**打码协议**（前后端唯一约定，改它要两边一起改）：secret 字段回显成 `••••` + 尾 4 位；前端原样带回表示「没改」，后端在 `mergeConfigs` 里遇到 `••••` 前缀就保留旧值。这样既不会把真 key 发到浏览器，也不会因为打码串把已存的 key 洗掉。

**测试连接不打 Runner**：Runner 要求「已保存且已启用」，而这里恰恰要在保存之前验证配置，所以直接用 `Host.Invoke`（同一个宿主、同一套超时与错误分类），只回前 3 条给人看。

验收实测：`go build ./...` 通过；`go test ./service/plugin ./service/agent/tools ./service/agent/toolgrant` 全绿；`vite build` 成功（新增组件无编译问题）。

**P4 治理**
限额（每工具并发 / 日调用）、`last_used_at` / `last_error` 列、密钥加密、宿主独立系统用户或容器、可选 `mode: fast|deep` 映射到具体实现。

---

## 11. 硬规则与风险

1. **工具名一旦发布不能改**：改名 = 新工具，旧授权悬空、`AGENTS.md` / 技能里写死的名字失效。要改就新增 + 迁移授权。
2. **`tool.py` 的 schema 是模型契约**：加可选参数安全；删/改必填参数会直接影响已授权 agent。
3. **插件代码以 nexus 进程权限运行**（管理员部署的代码，非不可信用户代码）。缓解：插件目录不在租户子树、不挂进沙箱、密钥只经 stdin 传入不落磁盘；P4 可换独立系统用户/容器。
4. **不做声明式 HTTP 模板引擎**：换后端靠写一个 `plugin.py`（几十行 Python），不靠用户填模板表达式 —— 后者一旦开口，鉴权/错误/裁剪全得用户负责且无法统一调试。
5. **不做常驻宿主**：真到瓶颈（局域网后端 20ms 往返）再升级为「常驻 + 每调用 fork」，形态与 sandboxd 的 scripthost 一致。

---

## 12. 操作对照表（给运维/实施看）

| 想做的事 | 动什么 | 生效时机 |
|---|---|---|
| 换搜索后端 | 管理端卡片改「主实现」 | 下次调用 |
| 加一家兜底 | 管理端卡片加兜底项 | 下次调用 |
| 加新工具 `web_fetch` | 磁盘加 `tools/web_fetch/{tool.py,impl/plugin.py}` | 60s / 刷新后可见；启用+授权后可用 |
| 给 `web_search` 加 Brave | 磁盘加 `web_search/brave/plugin.py` | 60s / 刷新后出现在下拉里 |
| 改工具描述 / schema | 改 `tool.py` | 刷新后对新请求生效 |
| 停用工具 | 管理端关「启用」 | 立即（目录变灰、调用被拒） |
| 彻底移除 | 先停用，再删磁盘目录 | 停用立即；目录 60s 内消失 |
