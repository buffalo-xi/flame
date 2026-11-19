// Package capability defines the keys of a "tool capability": platform grants, tool-catalog availability and the runtime gate all share
// one set.
//
// The key prefix is still sandbox.* (the database and the API both use it, and a rename would change copy, not keys), but the meaning is
// "may this kind of tool be used", not "is the sandbox on" - later capabilities such as egress (net.egress) land here too.
//
// The tiers are split by **process shape** (not by "what can it run"): all four can execute arbitrary code inside the sandbox
// (os.system / subprocess in a script is equivalent to exec); what differs is the lifetime and the resource gate:
//
//	sandbox.exec     one-shot process, exits as soon as it is done; no pool, no hard timeout (exec, skill script run_skill_script,
//	                 tenant custom tool custom__*)
//	sandbox.shell    session-level resident process, kept across calls / conversations, reclaimed only after an idle timeout
//	sandbox.code     hosted snippet execution (/v1/code channel: workflow code nodes / branch / binding), with concurrency slots and the
//	                 script_max_sec hard gate
//	sandbox.browser  resident heavyweight Chromium process, which inherently needs egress
//
// Why this tier is called code rather than script: **the exec channel also runs script files** (.py/.js/.lua/.sh), so naming it script
// would take a property both channels share and make it the name of one of them. The real difference is the contract:
//
//	exec  you supply the full argv (program + arguments); the interpreter and the I/O protocol are yours to decide;
//	code  you supply "language + snippet + inputs"; the server resolves the interpreter, and the snippet is woven into a complete
//	      program by a wrapper template (injecting input variables, turning print into stderr, writing the return value as
//	      {"result": X}).
//
// Nor is it called pool: pooling is a server-side resource implementation, not a contract - when the implementation changed (say codehost
// moving from "a resident worker pool" to "a reused isolation unit with a fresh interpreter per fork") the name would no longer be true.
// The /v1/code endpoint and the script.* configuration section (script_max_sec / script.pools) are deliberately not named alike: the
// former is the call contract, the latter is a sandboxd implementation parameter.
//
// The criterion is the **channel**, not "is a script file being run": tenant custom tools (custom__*) and skill scripts both go through
// /v1/exec and belong to the exec tier; only the /v1/code channel belongs to code. Having script in the name easily leads the other way
// (historically custom tools were mis-filed under sandbox.script, so turning off workflow scripts turned the tools off with them).
//
// ⚠️ Do not advertise it as a permission boundary: turning exec off does not mean "cannot execute code", since the code tier can spawn
// subprocesses just as well. Its job is to **grant resources by shape**; the real isolation boundary lives in the sandbox implementation.
//
// Egress is another dimension: it does not describe a tool shape but a **cross-channel egress policy** - whether the command-execution
// family (exec / shell / skill scripts) may reach the network. It hangs off the execution capabilities (it is only meaningful when exec
// or shell is on, see capabilityDeps in the platform handler) without belonging to either; browser brings its own egress and follows
// sandbox.browser, while the workflow code channel is always offline and unaffected by it.
//
// The mapping criterion rests on the tool category (see tools.CapabilityOf); this package does not import tools - it is a leaf package
// that tools and shared/capabilitypolicy both import.
package capability

const (
	Exec    = "sandbox.exec"
	Shell   = "sandbox.shell"
	Code    = "sandbox.code"
	Browser = "sandbox.browser"

	// Egress lets the command-execution family reach the network (the deployment side has a second ceiling; the two are ANDed).
	//
	// The key uses net.* rather than sandbox.*: it is not "may this kind of tool be used" but a cross-channel egress permission, so any
	// future egress scenario (a plugin callback, say) belongs to this tier too.
	Egress = "net.egress"
)

// LegacyScript is the old key (renamed from sandbox.script to sandbox.code in 2026-10).
//
// It is used for **transitional reads** only: old rows may still be in the database, and Normalize takes care of them.
// Once the migration script (nexus/platform/sql/capability_code_rename.sql) has run and no old rows remain, delete this together with the
// legacy branch in Normalize and Valid - keeping it is a second source of truth.
const LegacyScript = "sandbox.script"

// Normalize maps an old key to the new one; anything else is returned unchanged.
//
// Both read paths (Resolve / ResolveAll / Gates) and write paths (Set / SetGate) must go through it: normalising only the reads would
// read the old key while writing the new one, leaving two synonymous rows in the database.
func Normalize(key string) string {
	if key == LegacyScript {
		return Code
	}
	return key
}

// All returns every known capability (in a stable order: both the list rendering and the migration script follow it).
//
// It lists **new keys only**: old keys are handled by Normalize and must not appear here, or the admin side would show an extra card with
// the same name and a different key.
//
// Egress comes after the four tiers: the first four are "tiers by process shape", while it is a **cross-channel egress policy** (it
// constrains exec / shell / skill scripts, not browser or workflow code).
//
// Do not put it right after exec or hang it under exec: its scope includes shell, and shell is exec's sibling - nesting it would mean a
// child governing its sibling, which reads as a contradiction.
func All() []string { return []string{Exec, Shell, Code, Browser, Egress} }

// Valid reports whether a key is legal: the HTTP layer and the policy layer share one criterion, so a dirty key cannot be stored.
//
// During the transition the old key (LegacyScript) is accepted too: the frontend takes new keys from All(), but external scripts / CI may
// still send the old one, and rejecting it outright would be a pointless 400. Writes are normalised by Normalize, so no old key is ever
// stored.
func Valid(key string) bool {
	switch key {
	case Exec, Shell, Code, Browser, Egress:
		return true
	case LegacyScript: // transitional compatibility, deleted together with Normalize
		return true
	}
	return false
}

// Label is the human-readable name: for the **platform UI** (the admin capability cards, the reason a catalog entry is greyed out).
//
// Each tier describes only its "shape", never "what it can run" - otherwise it reads as if a tier restricted a language or a command. The
// scenario list in parentheses is for administrators; for an **execution-layer error** use ShortLabel.
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
	// Egress only describes the "command-execution family": browser brings its own egress (following sandbox.browser), the workflow code
	// channel is always offline, and neither is controlled by this key.
	case Egress:
		return "出网（exec / shell / 技能脚本可访问外网）"
	}
	return key
}

// ShortLabel is the minimal name: it describes the shape only, with no scenario list.
//
// It is used in **execution-layer errors**, where the sentence must line up with what the caller just did. Once the tier and the scenario
// drift apart, Label's "workflow code node / branch / binding" misleads - historically custom tools were mis-filed under
// sandbox.script while the error told the administrator to configure a workflow, which had nothing to do with what was happening.
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
