// Package capability 定义「工具能力」的键名：平台授权、工具目录可用性、运行期闸门共用一套。
//
// 键名沿用 sandbox.*（数据库与接口都是这个口径，改名只换文案不动键），但语义是
// 「这类工具能不能用」，不是「沙箱开不开」—— 后续出网（net.egress）等能力同样落在这里。
//
// 按**进程形态**划档（不是按「能跑什么」）：四档都能在沙箱里执行任意代码
// （脚本里的 os.system / subprocess 与 exec 等价），区别是生命周期与资源闸：
//
//	sandbox.exec     一次性进程，起完即退，无池、无硬超时（exec、技能脚本 run_skill_script、租户自定义工具 custom__*）
//	sandbox.shell    会话级常驻进程，跨调用/跨对话保留，空闲超时才回收
//	sandbox.code     托管式代码片段执行（/v1/code 通道：工作流 code 节点 / 分支 / binding），有并发槽与 script_max_sec 硬闸
//	sandbox.browser  Chromium 常驻重量级进程，且天然需要出网
//
// 为什么这一档叫 code 而不是 script：**exec 通道同样在执行脚本文件**（.py/.js/.lua/.sh），
// 用 script 命名等于把两条通道共有的性质当成其中一条的名字。真正的区别在契约：
//
//	exec  你给完整 argv（程序 + 参数），解释器与输入输出协议都由你定；
//	code  你给「语言 + 代码片段 + inputs」，解释器由服务端解析、片段被包装模板织成
//	      完整程序（注入 input 变量、print 转 stderr、返回值写 {"result": X}）。
//
// 也不叫 pool：池化是服务端的资源实现，不是契约 —— 实现变了（如 codehost 从
// 「常驻 worker 池」改为「复用隔离单元 + 每次 fork 新解释器」）名字就会失真。
// 端点 /v1/code 与配置段 script.*（script_max_sec / script.pools）刻意不同名：
// 前者是调用契约，后者是 sandboxd 的实现参数。
//
// 判据是**通道**而不是「跑的是不是脚本文件」：租户自定义工具（custom__*）与技能脚本都走
// /v1/exec，归 exec 档；只有 /v1/code 通道归 code 档。名字里带 script 容易反过来
// 诱导归错档（历史上自定义工具就误挂过 sandbox.script，导致关工作流脚本时把工具一起关掉）。
//
// ⚠️ 不要把它当成权限边界来宣传：关掉 exec 不等于「不能执行代码」，code 档一样能起
// 子进程。它的作用是**按形态授权资源**，真正的隔离边界在沙箱实现里。
//
// Egress 是另一个维度：它不描述工具形态，而是**跨通道的出口策略** —— 命令执行族
// （exec / shell / 技能脚本）能否访问外网。它依附于执行能力（exec 或 shell 任一开通
// 才有意义，见 platform/handler 的 capabilityDeps），但不从属于其中任何一档；
// browser 自带出网随 sandbox.browser 走，工作流 code 通道恒断网、不受它控制。
//
// 映射判据落在工具分类上（见 tools.CapabilityOf），本包不 import tools ——
// 它是叶子包，tools 与 shared/capabilitypolicy 都 import 它。
package capability

const (
	Exec    = "sandbox.exec"
	Shell   = "sandbox.shell"
	Code    = "sandbox.code"
	Browser = "sandbox.browser"

	// Egress 允许命令执行族访问外网（部署侧还有一层上限，两者是 AND）。
	//
	// 键名走 net.* 而不是 sandbox.*：它不是「某类工具能不能用」，而是跨通道的出口权限，
	// 将来若出现其它出网场景（如插件回调）也归这一档。
	Egress = "net.egress"
)

// LegacyScript 旧键（2026-10 由 sandbox.script 更名为 sandbox.code）。
//
// 只用于**过渡期读取**：库里可能还留着旧行，Normalize 负责归一。
// 迁移脚本（nexus/platform/sql/capability_code_rename.sql）跑完、确认无旧行后，
// 连同 Normalize 与 Valid 里的旧键分支一起删除 —— 留着就是第二套真相。
const LegacyScript = "sandbox.script"

// Normalize 把旧键归一为新键；其它键原样返回。
//
// 读路径（Resolve / ResolveAll / Gates）与写路径（Set / SetGate）都要过它：
// 只归一读不归一写，会一边读出旧键、一边写进新键，库里出现两行同义记录。
func Normalize(key string) string {
	if key == LegacyScript {
		return Code
	}
	return key
}

// All 全部已知能力（顺序稳定：列表渲染与迁移脚本都按它走）。
//
// 只列**新键**：旧键靠 Normalize 兼容，不能出现在这里，否则管理端会多出一行
// 同名不同键的卡片。
//
// Egress 排在四档之后：前四档是「按进程形态分档」，它则是**跨通道的出口策略**
// （约束 exec / shell / 技能脚本，不约束 browser 与工作流 code）。
//
// 不要把它排进 exec 后面或挂在 exec 底下：它的作用域包含 shell，而 shell 是 exec 的
// 兄弟，嵌进去就变成「子项管兄弟」，读起来自相矛盾。
func All() []string { return []string{Exec, Shell, Code, Browser, Egress} }

// Valid 键名是否合法：HTTP 层与策略层共用一个判据，避免脏 key 落库。
//
// 过渡期同时接受旧键（LegacyScript）：前端从 All() 拿新键，但外部脚本 / CI 可能还在
// 传旧键，直接拒会变成一次无谓的 400。写入时由 Normalize 归一，不会落旧键。
func Valid(key string) bool {
	switch key {
	case Exec, Shell, Code, Browser, Egress:
		return true
	case LegacyScript: // 过渡期兼容，随 Normalize 一起删
		return true
	}
	return false
}

// Label 人话名称：**平台界面**用（管理端能力卡片、目录置灰原因）。
//
// 每档只描述「形态」，不描述「能跑什么」—— 否则会让人以为某一档限制了语言/命令。
// 括注里的场景枚举是给管理员看的；要放进**执行层报错**请用 ShortLabel。
func Label(key string) string {
	switch Normalize(key) {
	case Exec:
		return "一次性执行（exec / 技能脚本 / 自定义工具，起完即退）"
	case Shell:
		return "常驻会话（shell，进程跨调用保留）"
	case Code:
		return "代码片段执行（工作流 code 节点 / 分支 / binding，服务端托管解释器）"
	case Browser:
		return "浏览器（browser，Chromium 常驻进程）"
	// 出网只描述「命令执行族」：浏览器自带出网（随 sandbox.browser），
	// 工作流 code 通道恒断网，两者都不由这个键控制。
	case Egress:
		return "出网（exec / shell / 技能脚本可访问外网）"
	}
	return key
}

// ShortLabel 极简名称，只描述形态、不带场景枚举。
//
// 用在**执行层报错**里：那句话要能直接对上调用方刚做的事。Label 里的
// 「工作流 code 节点 / 分支 / binding」一旦档位与场景有出入就会误导 ——
// 历史上自定义工具误挂 sandbox.script，报错却提示管理员去配工作流，与现场完全无关。
func ShortLabel(key string) string {
	switch Normalize(key) {
	case Exec:
		return "一次性执行"
	case Shell:
		return "常驻会话"
	case Code:
		return "代码片段执行"
	case Browser:
		return "浏览器"
	case Egress:
		return "出网"
	}
	return key
}
