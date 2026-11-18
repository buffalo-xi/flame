package codehost

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// langTemplate 描述一种语言「怎么把用户脚本包成可独立运行、并能回传结果的完整脚本」。
//
// codehost 是语言无关的常驻宿主：一份代码 fork 三种解释器。每次 execute 都按
// lang 选中模板、把用户脚本与 inputs 织进包装脚本，再写盘交给一个**全新** fork 的
// 解释器进程执行。包装脚本承担三件事：
//  1. 把 inputs 注入成脚本可见的 input 变量；
//  2. 把 print/log/console 这类「人看的输出」重定向到 stderr（stdout 留给协议）；
//  3. 捕获脚本的「返回值」，以 JSON 写回 stdout（{"result": X} / {"error": msg}）。
type langTemplate struct {
	ext   string
	build func(script string, inputs json.RawMessage) string
}

// templates 语言 → 包装模板。
var templates = map[string]*langTemplate{
	"lua":    {ext: ".lua", build: buildLua},
	"python": {ext: ".py", build: buildPython},
	"js":     {ext: ".js", build: buildJS},
}

// normalizeInputs 把缺失 / null 的 inputs 归一成空对象。
//
// 为什么是 {} 而不是各语言自己的「空」：脚本几乎都假设 input 是张表 / 字典，
// 传 null 会让 input == nil / None / null，脚本写 input.x 直接炸，而且三种语言
// 的表现还不一致。空对象是最省心、最一致的缺省。
func normalizeInputs(inputs json.RawMessage) []byte {
	if len(inputs) == 0 || string(inputs) == "null" {
		return []byte("{}")
	}
	return inputs
}

// ---------- Python ----------

// buildPython 生成可独立运行的 Python 脚本。inputs 用 base64 携带（彻底避开
// 字符串字面量转义问题），用户脚本用 strconv.Quote 包成合法 Python 字符串字面量，
// 经 compile + exec 执行，返回值走 __return__ 约定。
//
// 编译单元的文件名刻意写死成 "<script>"：这样 traceback 里的行号就是**用户脚本自己的
// 行号**，和编辑器里看到的第 N 行一致。代价是文件名不对应磁盘上的任何文件。
//
// 不要为了「让文件名指向真实文件」而改成把脚本拼进包装脚本、或单独写 main.py 再 import
// —— 那样行号会加上包装头的偏移（脚本第 3 行变成文件第 23 行），对用户反而更糟。
func buildPython(script string, inputs json.RawMessage) string {
	b64 := base64.StdEncoding.EncodeToString(normalizeInputs(inputs))
	var b strings.Builder
	b.WriteString(pyHead)
	b.WriteString(b64)
	b.WriteString(pyMid)
	b.WriteString(strconv.Quote(script))
	b.WriteString(pyTail)
	return b.String()
}

const pyHead = `import sys, json, base64, traceback, builtins

def _sc_print(*args, **kwargs):
    kwargs.setdefault("file", sys.stderr)
    return builtins.print(*args, **kwargs)

_input = json.loads(base64.b64decode("`

const pyMid = `").decode("utf-8"))
_g = {"input": _input, "print": _sc_print, "log": _sc_print, "__return__": None}
try:
    exec(compile(`

const pyTail = `, "<script>", "exec"), _g)
except Exception as _e:
    traceback.print_exc(file=sys.stderr)
    sys.stdout.write(json.dumps({"error": str(_e)}))
    sys.exit(1)
_r = _g.get("__return__")
if _r is None:
    sys.stdout.write("{}")
else:
    try:
        sys.stdout.write(json.dumps({"result": _r}))
    except Exception:
        sys.stdout.write(json.dumps({"result": str(_r)}))
`

// ---------- Node.js ----------

// buildJS 生成可独立运行的 Node 脚本。用户脚本以源码原样织进一个 async IIFE 体内，
// 所以脚本可以直接 return / await。返回值走 JSON.stringify，console 重定向到 stderr。
//
// 已知取舍：脚本的**语法错误**会让整份文件在解析期失败（IIFE 根本没机会跑），
// 错误信息只能靠 stderr 兜底（见 execute 的 fallbackError），不像运行期错误能被
// .catch 抓住。运行期错误与 Python / Lua 一样有干净的 message。
func buildJS(script string, inputs json.RawMessage) string {
	b64 := base64.StdEncoding.EncodeToString(normalizeInputs(inputs))
	var b strings.Builder
	b.WriteString(jsHead)
	b.WriteString(b64)
	b.WriteString(jsMid)
	b.WriteString(script)
	b.WriteString(jsTail)
	return b.String()
}

const jsHead = `function _sc_str(v) {
  if (v === null) return "null";
  if (v === undefined) return "undefined";
  if (typeof v === "object") {
    try { return JSON.stringify(v); } catch (e) { return String(v); }
  }
  return String(v);
}
function _sc_log(...args) {
  process.stderr.write(args.map(_sc_str).join("\t") + "\n");
}
const _input = JSON.parse(Buffer.from("`

const jsMid = `", "base64").toString("utf8"));
const _console = { log: _sc_log, info: _sc_log, warn: _sc_log, error: _sc_log, debug: _sc_log };

(async () => {
  let input = _input;
  let log = _sc_log;
  let print = _sc_log;
  let console = _console;

`

const jsTail = `
})().then((_result) => {
  if (_result === undefined) {
    process.stdout.write("{}");
  } else {
    try {
      process.stdout.write(JSON.stringify({ result: _result }));
    } catch (e) {
      process.stdout.write(JSON.stringify({ result: String(_result) }));
    }
  }
}).catch((_e) => {
  process.stderr.write((_e && _e.stack) ? _e.stack + "\n" : String(_e) + "\n");
  process.stdout.write(JSON.stringify({ error: (_e && _e.message) ? _e.message : String(_e) }));
  process.exitCode = 1;
});
`

// ---------- Lua ----------

// buildLua 生成可独立运行的 Lua 脚本。inputs 由 Go 侧翻译成 Lua 表字面量（避开在
// 模板里塞 JSON 解码器），用户脚本用长括号字符串原样嵌入 load，返回值走第一个返回值。
//
// 与 Python / Node 不同，Lua 没有标准库 JSON：结果回传需要内嵌一个极简 JSON 编码器
// （见 luaEncoder），让 codehost 完全自包含、不依赖 /workers 下的 dkjson。
func buildLua(script string, inputs json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(normalizeInputs(inputs)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		v = map[string]any{}
	}

	var b strings.Builder
	b.WriteString(luaEncoder)
	b.WriteString("local input = ")
	b.WriteString(luaLiteral(v))
	b.WriteString(luaExecHead)
	b.WriteString(luaLongBracket(script))
	b.WriteString(luaExecTail)
	return b.String()
}

// luaEncoder 极简 JSON 编码器（内嵌进包装脚本，保证 Lua 侧自包含）。
const luaEncoder = `local function _sc_json_quote(_s)
  local _out = {'"'}
  for _i = 1, #_s do
    local _c = _s:sub(_i, _i)
    local _b = _c:byte()
    if _c == '"' then _out[#_out + 1] = '\\"'
    elseif _c == '\\' then _out[#_out + 1] = '\\\\'
    elseif _b == 8 then _out[#_out + 1] = '\\b'
    elseif _b == 9 then _out[#_out + 1] = '\\t'
    elseif _b == 10 then _out[#_out + 1] = '\\n'
    elseif _b == 12 then _out[#_out + 1] = '\\f'
    elseif _b == 13 then _out[#_out + 1] = '\\r'
    elseif _b < 32 then _out[#_out + 1] = string.format('\\u%04x', _b)
    else _out[#_out + 1] = _c end
  end
  _out[#_out + 1] = '"'
  return table.concat(_out)
end

local function _sc_json_encode(_v, _seen)
  local _t = type(_v)
  if _v == nil then return "null" end
  if _t == "boolean" then return _v and "true" or "false" end
  if _t == "number" then
    if _v ~= _v or _v == math.huge or _v == -math.huge then return "null" end
    if _v == math.floor(_v) and math.abs(_v) < 1e15 then
      return string.format("%d", _v)
    end
    return string.format("%.17g", _v)
  end
  if _t == "string" then return _sc_json_quote(_v) end
  if _t == "table" then
    if next(_v) == nil then return "{}" end
    _seen = _seen or {}
    if _seen[_v] then return '""' end
    _seen[_v] = true
    local _isarr = true
    local _max = 0
    for _k in pairs(_v) do
      if type(_k) ~= "number" or _k < 1 or _k ~= math.floor(_k) then
        _isarr = false
        break
      end
      if _k > _max then _max = _k end
    end
    local _parts = {}
    if _isarr then
      for _i = 1, _max do
        _parts[_i] = _sc_json_encode(_v[_i], _seen)
      end
      _seen[_v] = nil
      return "[" .. table.concat(_parts, ",") .. "]"
    end
    local _n = 0
    for _k, _val in pairs(_v) do
      _n = _n + 1
      _parts[_n] = _sc_json_quote(tostring(_k)) .. ":" .. _sc_json_encode(_val, _seen)
    end
    _seen[_v] = nil
    return "{" .. table.concat(_parts, ",") .. "}"
  end
  return _sc_json_quote(tostring(_v))
end

`

// luaExecHead 在 input 字面量之后、用户脚本长括号之前的部分。
const luaExecHead = `
local _env = setmetatable({ input = input }, { __index = _G })

local function _sc_log(...)
  local _parts = {}
  for _i = 1, select("#", ...) do
    local _v = select(_i, ...)
    if type(_v) == "table" then
      local _ok, _s = pcall(_sc_json_encode, _v)
      _parts[_i] = _ok and _s or tostring(_v)
    else
      _parts[_i] = tostring(_v)
    end
  end
  io.stderr:write(table.concat(_parts, "\t"), "\n")
end
_env.log = _sc_log
_env.print = _sc_log

local _fn, _lerr = load(`

// luaExecTail 用户脚本长括号之后、到脚本末尾的部分。
const luaExecTail = `, "=script", "t", _env)
if not _fn then
  io.stderr:write(tostring(_lerr), "\n")
  io.stdout:write(_sc_json_encode({ error = tostring(_lerr) }))
  os.exit(1)
end

local _res = table.pack(xpcall(_fn, debug.traceback))
if _res[1] then
  if _res.n > 1 and _res[2] ~= nil then
    io.stdout:write(_sc_json_encode({ result = _res[2] }))
  else
    io.stdout:write("{}")
  end
else
  local _err = tostring(_res[2])
  io.stderr:write(_err, "\n")
  io.stdout:write(_sc_json_encode({ error = _err }))
  os.exit(1)
end
`

// luaLiteral 把已解码的 inputs（any）翻译成 Lua 表字面量。
//
// 用 JSON 解码结果而不是 json.RawMessage 原文：Lua 没有 JSON 字面量，必须由 Go 侧
// 生成 Lua 语法。数字经 UseNumber 保留成 json.Number，避免大整数被 float64 吃掉精度。
func luaLiteral(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return luaQuote(x)
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = luaLiteral(e)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(x))
		for _, k := range keys {
			parts = append(parts, "["+luaQuote(k)+"] = "+luaLiteral(x[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return "nil"
	}
}

// luaQuote 生成一个合法的 Lua 双引号字符串字面量（字节级转义，UTF-8 原样透传）。
func luaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 32:
			b.WriteByte('\\')
			b.WriteString(pad3(int(c)))
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pad3 把 0-255 转成 3 位十进制（Lua 的 \ddd 转义）。
func pad3(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// luaLongBracket 把脚本包成长括号字符串（[=[...]=]），动态加 '=' 直到关闭序列
// 不出现在脚本里，彻底避开转义。
func luaLongBracket(s string) string {
	n := 0
	for {
		closer := "]" + strings.Repeat("=", n) + "]"
		if !strings.Contains(s, closer) {
			return "[" + strings.Repeat("=", n) + "[" + s + closer
		}
		n++
	}
}
