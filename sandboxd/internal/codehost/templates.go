package codehost

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// langTemplate describes how a language "wraps a user script into a complete, independently runnable script that can
// also report its result back".
//
// codehost is a language-agnostic resident host: one body of code forks three interpreters. Every execute selects the
// template by lang, weaves the user script and the inputs into a wrapper script, writes it to disk and hands it to a
// **freshly forked** interpreter process. The wrapper script takes on three jobs:
//  1. inject the inputs as the `input` variable the script sees;
//  2. redirect print/log/console — "output for humans" — to stderr (stdout is reserved for the protocol);
//  3. capture the script's "return value" and write it back to stdout as JSON ({"result": X} / {"error": msg}).
type langTemplate struct {
	ext   string
	build func(script string, inputs json.RawMessage) string
}

// templates maps a language to its wrapper template.
var templates = map[string]*langTemplate{
	"lua":    {ext: ".lua", build: buildLua},
	"python": {ext: ".py", build: buildPython},
	"js":     {ext: ".js", build: buildJS},
}

// normalizeInputs normalises missing / null inputs into an empty object.
//
// Why {} rather than each language's own "empty": scripts almost always assume input is a table / dict, and passing
// null makes input == nil / None / null, so `input.x` blows up immediately — and the three languages do not even
// behave the same way. An empty object is the least surprising, most consistent default.
func normalizeInputs(inputs json.RawMessage) []byte {
	if len(inputs) == 0 || string(inputs) == "null" {
		return []byte("{}")
	}
	return inputs
}

// ---------- Python ----------

// buildPython generates an independently runnable Python script. The inputs are carried in base64 (sidestepping
// string-literal escaping entirely), and the user script is wrapped by strconv.Quote into a valid Python string
// literal, executed through compile + exec, with the return value following the __return__ convention.
//
// The compile unit's filename is deliberately hardcoded to "<script>": that way the line numbers in a traceback are
// **the line numbers of the user's own script**, matching the Nth line seen in the editor. The cost is that the
// filename does not correspond to any file on disk.
//
// Do not change it to splicing the script into the wrapper, or writing a separate main.py and importing it, just to
// "make the filename point at a real file" — that would offset every line number by the wrapper header (line 3 of
// the script becoming line 23 of the file), which is worse for the user.
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

// buildJS generates an independently runnable Node script. The user script is woven into an async IIFE body as-is, so
// it can use return / await directly. The return value goes through JSON.stringify, and console is redirected to
// stderr.
//
// Known trade-off: a **syntax error** in the script makes the whole file fail at parse time (the IIFE never gets a
// chance to run), so the message can only be recovered from stderr (see the stdout fallback in reply) — unlike a
// runtime error, which .catch can capture. Runtime errors get a clean message, same as Python / Lua.
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

// buildLua generates an independently runnable Lua script. The inputs are translated into a Lua table literal on the
// Go side (avoiding stuffing a JSON decoder into the template), and the user script is embedded as a long-bracket
// string for load, with the first return value used as the result.
//
// Unlike Python / Node, Lua has no standard-library JSON: reporting the result needs a minimal JSON encoder embedded
// (see luaEncoder), keeping codehost fully self-contained and independent of dkjson under /workers.
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

// luaEncoder is a minimal JSON encoder (embedded in the wrapper script, keeping the Lua side self-contained).
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

// luaExecHead is the part after the input literal and before the user script's long bracket.
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

// luaExecTail is the part after the user script's long bracket, up to the end of the script.
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

// luaLiteral translates the decoded inputs (any) into a Lua table literal.
//
// It uses the JSON decoding result rather than the raw json.RawMessage bytes: Lua has no JSON literals, so the Lua
// syntax must be generated on the Go side. Numbers go through UseNumber and stay json.Number, so large integers are
// not rounded away by float64.
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

// luaQuote produces a valid Lua double-quoted string literal (byte-level escaping, UTF-8 passed through as-is).
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

// pad3 turns 0-255 into 3 decimal digits (Lua's \ddd escape).
func pad3(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// luaLongBracket wraps the script into a long-bracket string ([=[...]=]), adding '=' dynamically until the closing
// sequence does not appear inside the script, sidestepping escaping entirely.
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
