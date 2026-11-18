# `http` 节点

发一次 HTTP 请求，支持 JSON / 表单体与 SSE 流式响应。

## config 形状

```json
{
  "http": {
    "url": "https://api.example.com/search",
    "method": "POST",
    "headers": [{"key": "Authorization", "value": "Bearer {{ .input.token }}"}],
    "params": [{"key": "q", "value": "{{ .input.q }}"}],
    "body_type": "json",
    "body": "{\"q\": \"{{ .input.q }}\"}",
    "timeout": {"enabled": true, "seconds": 30},
    "response_mode": ""
  },
  "inputs": {"q": {"type": "string", "source_type": "argument", "source_key": "q"}},
  "outputs": {
    "body": {"type": "string", "description": "响应体"},
    "mode": {"type": "string", "description": "响应模式"},
    "status": {"type": "number", "description": "HTTP 状态码"},
    "headers": {"type": "string", "description": "响应头"},
    "sse_events": {"type": "array[string]", "description": "SSE 事件列表"},
    "stream_finished": {"type": "boolean", "description": "流式传输是否完成"}
  }
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `http.url` | ✅ | — | 支持模板 |
| `http.method` | — | `GET` | |
| `http.headers` | — | `[]` | **`[{key,value}]` 数组**，不是对象；值支持模板 |
| `http.params` | — | `[]` | 追加到 query，同上数组结构 |
| `http.body` | — | 空 | 支持模板 |
| `http.body_type` | — | `json` | `json` / `form`，只决定缺省 `Content-Type` |
| `http.response_mode` | — | 空 = 自动 | 空时按响应 `Content-Type: text/event-stream` 判定；也可强制 `complete` / `sse_stream` |
| `http.timeout` | — | 关闭 | `{enabled, seconds}`；关闭（或未配置）时不设节点级超时，由运行上限兜底 |
| `http.handler` | — | — | 后处理脚本 `{script, lang, timeout{enabled,seconds}}`；**不支持 `template`** |
| `config.inputs` | — | — | 模板来源 |
| `config.outputs` | 建议写全 | 执行器补齐 | 六个固定变量；漏了也能跑 |

## 输出变量

固定：`mode` / `status` / `headers` / `body` / `sse_events` / `stream_finished`。
`handler` 在 complete 模式跑一次，SSE 模式每个事件跑一次 + 收尾一次，只能写自定义变量。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 凭据：优先引用全局变量

做法是先在 `config.inputs` 里加一项来源为全局变量的输入，再在 header 里引用它：

```json
"inputs":  { "token": {"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"} },
"headers": [{"key": "Authorization", "value": "Bearer {{ .input.token }}"}]
```

值不落在流程定义里，资产导出只带引用名，装到别的租户后由对方的变量值填充。
**不要**写 `{{ .global_vars.X }}`：这个写法已下线，流程载入时直接报错。

不要直接把 token / 口令写进 `headers` / `params` / `body` —— 上架时会被按键名清空（安装方还得重填），
跨租户带走就是泄漏。

## 常见错误

- `url is required` —— 没写 `url`（或渲染后为空）。
- 连接失败/超时 → `connection` / `timeout`，要兜就在 `on_error.retry.codes` 里写上这两个码。
- SSE 场景下游拿不到 `body` —— 流式时正文在 `sse_events` 里，`body` 可能为空。

源码：`service/flow/executors/http.go`
