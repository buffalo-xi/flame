package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
)

// Environment 一次能力探测的结果（经 /v1/capabilities 原样交给 nexus）。
//
// Text 是若干行文本（各探针自己的输出）：**刻意不解析成结构** ——「沙箱里有什么」
// 这份认知只该由沙箱自己持有（与「解释器入口名只该由 sandboxd 知道」同源），
// 而读它的本来就是 LLM。
type Environment struct {
	Text     string    `json:"text"`
	ProbedAt time.Time `json:"probed_at"`
	// Error 非空表示探测整体失败，Text 不可信。nexus 侧应当据此把能力报成
	// 「未知」，而不是默认成「没有」—— 猜错比说不知道更糟。
	Error string `json:"error,omitempty"`
}

// probe 一条探针：在沙箱内跑一个命令，收它的 stdout。
type probe struct {
	label string   // 输出前缀（仅失败提示用；成功的行由命令自己带前缀）
	argv  []string // 沙箱内的命令 + 参数
	// post 可选的「行级整理」：只做排版（丢噪声行、去重、补统一前缀），
	// 不解析成结构、不推断结论 —— 谁产生这份事实，谁负责把它讲清楚。
	post func(string) string
}

// probeLangOrder 探针的语言顺序。
//
// 固定顺序而非遍历 map：探针是并发跑的，汇总时必须按稳定顺序拼接，
// 否则每次启动给 agent 的文本都不一样（无谓的噪声）。
var probeLangOrder = []string{"python", "js", "lua"}

// langSelfReport 语言名 → 「怎么问它自己」（参数，**不含命令名**）。
//
// ⚠️ 命令名刻意不在这里：同一个名字在三种 mode 下未必都存在（镜像里是 Dockerfile
// `ln` 出来的 lua，rootfs 里可能只有 lua5.4，Windows 宿主上是带版本号的文件名），
// 那份认知的真源是 langs.<lang>.commands（见 config.go 的 CommandByMode），
// 取当前 mode 的解析结果即可 —— 在这里再写一遍就是第二份认知。
//
// 每种语言只用它的原生 API，不借 shell：探针在解释器里跑，不经过 sh，
// 于是「沙箱里有没有 sh / 参数怎么转义」都不成为问题（这正是逐条 argv 的形态
// 相对「一份 sh 脚本」的收益）。
var langSelfReport = map[string][]string{
	"python": {"-c", pySelfReport},
	"js":     {"-e", jsSelfReport},
	"lua":    {"-e", luaSelfReport},
}

// pySelfReport 自报解释器版本与全部已装包。
//
// importlib.metadata 是 Python 3.8+；老版本落到 except 里报 unavailable，
// 而不是让整条探测失败（能力缺失要如实说，但不能连带其它行一起崩）。
const pySelfReport = `import sys, importlib.metadata as m
print("Python 版本:", sys.version.split()[0])
try:
    names = []
    for d in m.distributions():
        n = d.metadata["Name"]
        if n:
            names.append(str(n) + "==" + str(d.version))
    print("已装 Python 包:", " ".join(sorted(names)) or "(none)")
except Exception as e:
    print("已装 Python 包: (unavailable:", e, ")")
`

// jsSelfReport 自报 node 版本与全局模块。
//
// 用 fs.readdirSync 读全局 node_modules，**不用 execSync("npm ls -g")**：
// 后者会把 shell 依赖重新引回来（正是逐条 argv 要消掉的东西），而沙箱里
// npm 全局包本来就是空目录（加包只能改 Dockerfile 重建镜像）。
const jsSelfReport = `const fs = require("fs");
console.log("Node 版本:", process.version);
const dirs = ["/usr/local/lib/node_modules", "/usr/lib/node_modules"].concat(
  (process.env.NODE_PATH || "").split(":").filter(Boolean));
const pkgs = new Set();
for (const d of dirs) {
  try { fs.readdirSync(d).forEach(n => { if (!n.startsWith(".")) pkgs.add(n); }); } catch (e) {}
}
console.log("全局 Node 包:", [...pkgs].sort().join(" ") || "(none)");
`

// luaSelfReport 自报 Lua 版本。
//
// 原本还报 package.path（模块搜索路径），去掉了：那是一串 `/usr/local/share/lua/5.4/?.lua`
// 模板路径（去重后仍占几百字符），对模型的决策没有用处 —— 它既不会去设 LUA_PATH，
// 也无法据此判断某个库能不能 require（那只能看沙箱里装没装）。而每次 agent 运行都会把
// 这段塞进上下文，换来的只是噪声。
//
// 注意与 js 那条的区别：js 报的是**读出来的模块名**，镜像里加了全局包就会体现出来；
// 这里报的是路径模板，就算往那些目录里塞了模块也看不出来 —— 所以它筛不出信息。
//
// 版本行留着：5.1 / 5.4 的语义差别很大（goto、整除、table.unpack、require 查找行为），
// 它直接影响脚本该怎么写。
const luaSelfReport = `local ver = _VERSION:gsub("^Lua ", "")
print("Lua 版本: " .. ver)
`

// probeEnvironment 探测「沙箱里有什么」。
//
//	mode=none       沙箱即宿主：走 Go 原生（GOOS/GOARCH + LookPath），不起任何子进程；
//	docker / nsjail 事实只有镜像 / rootfs 自己知道：在沙箱内逐条跑探针。
//
// 每条探针独立执行、独立失败：某条失败只让那一行标 (unavailable)，不影响其它行；
// 只有**全部都失败**才算整体失败（那说明沙箱根本起不来，而不是某个能力缺失）。
func (s *Server) probeEnvironment(ctx context.Context) Environment {
	env := Environment{ProbedAt: time.Now()}

	if s.cfg.Mode == isolate.NoneName {
		env.Text = s.probeHost()
		return env
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.Probe.TimeoutSec)*time.Second)
	defer cancel()

	probes := s.sandboxProbes()
	outcomes := make([]probeOutcome, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		// 并发跑：docker 下每条都是一次容器冷启动（300ms~1s），串行会把启动拖成好几秒
		go func(i int, p probe) {
			defer wg.Done()
			text, err := s.runProbe(ctx, p)
			if err == nil && p.post != nil {
				text = p.post(text)
			}
			outcomes[i] = probeOutcome{label: p.label, text: text, err: err}
		}(i, p)
	}
	wg.Wait()

	var b strings.Builder
	failed := 0
	for _, o := range outcomes {
		if o.err != nil {
			failed++
			fmt.Fprintf(&b, "%s: (unavailable: %v)\n", o.label, o.err)
			continue
		}
		b.WriteString(strings.TrimRight(o.text, "\n"))
		b.WriteByte('\n')
	}

	if failed > 0 && failed == len(outcomes) {
		// 全灭：沙箱不可用。此时任何输出都不可信，宁可不给。
		env.Error = fmt.Sprintf("all %d probes failed: %v", len(outcomes), outcomes[0].err)
		return env
	}
	// 末尾不留换行：这段 Text 会被 nexus 原样拼进 system prompt 的一个条目里，
	// 多一个 \n 就会在它与下一条说明之间顶出一个空行（纯噪声）。
	env.Text = strings.TrimRight(b.String(), "\n")
	return env
}

// probeOutcome 一条探针的结果（汇总时按序取用）。
type probeOutcome struct {
	label string
	text  string
	err   error
}

// sandboxProbes 组装本次要跑的探针：系统信息 + 各语言自报。
//
// 每条探针的输出都会原样进 agent 的上下文，所以行前缀一律写成「读者看得懂的话」
// （Python 版本 / 已装 Python 包 / …），而不是内部缩写：翻译放在产生方，
// nexus 侧继续零解析地透传（见 Environment.Text）。
func (s *Server) sandboxProbes() []probe {
	// os：os-release 是事实标准（base 镜像为 Debian，必然有）；万一没有，
	// 由 runProbe 报 unavailable —— 回退到 uname 交给 nexus 侧文案说明即可，
	// 不值得为极端环境再加一条探针（异常本身也是信息）。
	//
	// post 只做排版：整份 os-release 有十来行，其中 HOME_URL / SUPPORT_URL 之类
	// 对 agent 毫无信息量，却会随每一次 agent 运行进 LLM 上下文。
	probes := []probe{{
		label: "os",
		argv:  []string{"cat", "/etc/os-release"},
		post:  formatOSRelease,
	}}

	for _, lang := range probeLangOrder {
		args, ok := langSelfReport[lang]
		if !ok {
			continue
		}
		// 语言入口名按当前 mode 解析：顶层 langs 里没定义就跳过（那是「没配」，
		// 与「配了但沙箱里跑不起来」是两回事，不该报成 unavailable）。
		cmd, err := s.cfg.ResolveLangCommand(lang)
		if err != nil {
			continue
		}
		probes = append(probes, probe{label: lang, argv: append([]string{cmd}, args...)})
	}

	// 这里原本还有一条 luarocks 探针，删掉了：
	//   - 它在 read-only rootfs 下**必然**失败（luarocks 起手就要写临时文件，
	//     报 "unable to generate a unique filename"），所以永远只是一行 unavailable；
	//   - 失败原因是 500 字节的 Lua 栈回溯，对这行的可用性没有任何补充信息，
	//     白白占掉 LLM 上下文；
	//   - 镜像里本来就没装任何 rock，「Lua 有哪些第三方模块」的答案是「没有」，
	//     不值得为确认它再起一条探针（lua 那条也不报搜索路径了，见 luaSelfReport）。
	return probes
}

// runProbe 跑一条探针。
//
// 不挂载任何目录：探针只读系统信息，不需要工作区，也就不必碰 allowed_mount_roots。
// 走 s.exec（隔离实现）而不是宿主直跑 —— 探到的环境才是 agent 实际会得到的那个
// （只读 rootfs / 断网 / --user 都生效），否则就是另一个环境。
func (s *Server) runProbe(ctx context.Context, p probe) (string, error) {
	res, err := sandbox.Run(ctx, s.exec, &sandbox.Spec{
		Argv:   p.argv,
		Limits: sandbox.Limits{MaxOutputBytes: s.cfg.Probe.MaxOutputBytes},
	})
	if err != nil {
		return "", err
	}
	// 非零退出码不是 error（见 sandbox.Run 的约定），要自己判：
	// 命令不存在（127）、没有该文件（1）都在这里。
	if res.ExitCode != 0 {
		// 错误只取首行：工具报错常带多行栈回溯，而这一段会进 LLM 上下文，
		// 既破坏「每行一条」的形状，又没有信息量（原因首行已经写清）。
		return "", fmt.Errorf("exit %d: %s", res.ExitCode, firstLine(string(res.Stderr)))
	}
	return string(res.Stdout), nil
}

// probeHost 用 Go 原生探测宿主（mode=none：沙箱就是宿主本身）。
//
// 为什么不起 uname / cat 之类：宿主可能是 Windows（本机调试走的就是这条路），
// 那里没有 sh / cat / uname —— Go 自身的 GOOS/GOARCH 与 LookPath 跨平台且零依赖。
//
// 输出与沙箱分支保持同一套「每行一条」的形状，nexus 侧不必区分来源。
func (s *Server) probeHost() string {
	var b strings.Builder
	fmt.Fprintf(&b, "系统: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	for _, lang := range probeLangOrder {
		cmd, err := s.cfg.ResolveLangCommand(lang)
		if err != nil {
			continue
		}
		// 行前缀与沙箱分支保持同一套「读者看得懂」的风格；语言名沿用 langs 的键
		// （用户配置里看到的就是它），值换成宿主绝对路径 —— mode=none 下 agent
		// 真正会用的就是这条路径。
		if p, lerr := exec.LookPath(cmd); lerr == nil {
			fmt.Fprintf(&b, "%s 解释器: %s\n", lang, filepath.ToSlash(p))
		} else {
			// mode=none 下启动期 checkCommands 已经校验过一轮，这里只是防御。
			fmt.Fprintf(&b, "%s 解释器: (unavailable: 宿主 PATH 中找不到 %s)\n", lang, cmd)
		}
	}
	return b.String()
}

// formatOSRelease 把 /etc/os-release 整理成一行「系统: <名称> <版本>」。
//
// 只做排版：按 os-release 的既定格式取 PRETTY_NAME / ID / VERSION_ID，丢掉
// HOME_URL / SUPPORT_URL / BUG_REPORT_URL 之类对 agent 毫无信息量、却会随每次
// agent 运行进入 LLM 上下文的行。不推断任何结论（比如「这系统有没有 yum」）——
// 那是 agent 自己的事，说了就成了替它下结论。
//
// 认不出任何已知键时**原样返回**：宁可给一份没整理的原文，也不要凭空吞掉事实。
func formatOSRelease(text string) string {
	var pretty, id, ver string
	for _, line := range strings.Split(text, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"`) // os-release 的值多数带引号，去掉更像个名字
		switch key {
		case "PRETTY_NAME":
			pretty = val
		case "ID":
			id = val
		case "VERSION_ID":
			ver = val
		}
	}

	switch {
	case pretty != "":
		return "系统: " + pretty
	case id != "" && ver != "":
		return "系统: " + id + " " + ver
	case id != "":
		return "系统: " + id
	default:
		return strings.TrimRight(text, "\n")
	}
}

// firstLine 取首行并设长度上限。
//
// 用途只有一个：把「这条探针为什么没有结果」压成一行 —— 工具报错常带多行栈回溯
// （luarocks 是典型），整段塞进 Environment.Text 会破坏「每行一条」的形状，
// 也没有额外信息量。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "no output"
	}
	const maxErrChars = 200
	if len(s) > maxErrChars {
		s = s[:maxErrChars] + "..."
	}
	return s
}
