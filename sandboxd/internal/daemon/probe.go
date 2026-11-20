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

// Environment is the result of one capability probe (handed to nexus verbatim through /v1/capabilities).
//
// Text is a few lines of text (each probe's own output): it is **deliberately not parsed into a structure** —
// the knowledge of "what is inside the sandbox" should only be held by the sandbox itself (the same principle
// as "the interpreter entry name should only be known by sandboxd"), and whoever reads it is an LLM anyway.
type Environment struct {
	Text     string    `json:"text"`
	ProbedAt time.Time `json:"probed_at"`
	// A non-empty Error means the whole probe failed and Text must not be trusted. nexus should report the
	// capabilities as "unknown" because of it, not default to "none" — guessing wrong is worse than saying
	// "I do not know".
	Error string `json:"error,omitempty"`
}

// probe is one probe: run a command inside the sandbox and collect its stdout.
type probe struct {
	label string   // output prefix (only for failure notices; successful lines carry their own prefix)
	argv  []string // the command and arguments inside the sandbox
	// post is an optional line-level tidy-up: layout only (drop noise lines, de-duplicate, add a uniform
	// prefix) — no parsing into structures and no inferred conclusions: whoever produces the fact is
	// responsible for stating it clearly.
	post func(string) string
}

// probeLangOrder is the language order of the probes.
//
// A fixed order rather than iterating a map: the probes run concurrently, so the summary must be assembled in
// a stable order — otherwise every startup hands the agent different text (pointless noise).
var probeLangOrder = []string{"python", "js", "lua"}

// langSelfReport maps a language name to "how to ask it about itself" (the arguments, **excluding the command
// name**).
//
// ⚠️ The command name is deliberately absent: the same name does not necessarily exist under all three modes
// (in an image lua is a Dockerfile `ln` target, a rootfs may only have lua5.4, and a Windows host has a
// version-suffixed filename). The source of truth for that knowledge is langs.<lang>.commands (see
// CommandByMode in config.go); just use the resolution for the current mode — writing it again here would be a
// second copy of the same knowledge.
//
// Each language only uses its native API and never borrows a shell: the probe runs inside the interpreter, not
// through sh, so "is there an sh in the sandbox / how are arguments escaped" never becomes a problem (exactly
// the gain of argv lists over "a single sh script").
var langSelfReport = map[string][]string{
	"python": {"-c", pySelfReport},
	"js":     {"-e", jsSelfReport},
	"lua":    {"-e", luaSelfReport},
}

// pySelfReport reports the interpreter version and every installed package by itself.
//
// importlib.metadata is Python 3.8+; older versions fall into the except branch and report unavailable instead
// of failing the whole probe (a missing capability must be stated honestly, but must not take the other lines
// down with it).
//
// The output line prefixes are English on purpose: these lines are pasted into the agent's context verbatim (see
// Environment.Text) and are model-facing copy only — a human reads them through the model, never in a UI.
const pySelfReport = `import sys, importlib.metadata as m
print("Python version:", sys.version.split()[0])
try:
    names = []
    for d in m.distributions():
        n = d.metadata["Name"]
        if n:
            names.append(str(n) + "==" + str(d.version))
    print("Installed Python packages:", " ".join(sorted(names)) or "(none)")
except Exception as e:
    print("Installed Python packages: (unavailable:", e, ")")
`

// jsSelfReport reports the node version and global modules by itself.
//
// It reads the global node_modules with fs.readdirSync and **does not use execSync("npm ls -g")**: the latter
// brings the shell dependency back (exactly what argv lists are meant to remove), and global npm packages
// inside the sandbox are an empty directory anyway (adding packages means rebuilding the image).
const jsSelfReport = `const fs = require("fs");
console.log("Node version:", process.version);
const dirs = ["/usr/local/lib/node_modules", "/usr/lib/node_modules"].concat(
  (process.env.NODE_PATH || "").split(":").filter(Boolean));
const pkgs = new Set();
for (const d of dirs) {
  try { fs.readdirSync(d).forEach(n => { if (!n.startsWith(".")) pkgs.add(n); }); } catch (e) {}
}
console.log("Global Node packages:", [...pkgs].sort().join(" ") || "(none)");
`

// luaSelfReport reports the Lua version by itself.
//
// It used to report package.path (the module search path) too; that was removed: it is a list of
// `/usr/local/share/lua/5.4/?.lua` templates (hundreds of characters even after de-duplication) and useless for
// the model's decisions — it will not set LUA_PATH, and it cannot tell from those paths whether a library can
// be required (only whether it is installed in the sandbox can). And every agent run would paste that into the
// context, buying nothing but noise.
//
// Note the difference from the js probe: js reports **the module names it read**, so a global package added to
// the image shows up; this one reports path templates, where a module copied into those directories would stay
// invisible — which is why it filters nothing useful out.
//
// The version line stays: 5.1 and 5.4 differ substantially (goto, integer division, table.unpack, require
// lookup), which directly shapes how a script must be written.
const luaSelfReport = `local ver = _VERSION:gsub("^Lua ", "")
print("Lua version: " .. ver)
`

// probeEnvironment probes "what is inside the sandbox".
//
//	mode=none       the sandbox is the host: use native Go (GOOS/GOARCH + LookPath), spawn no child process;
//	docker / nsjail only the image / rootfs knows the facts: run the probes one by one inside the sandbox.
//
// Each probe runs and fails independently: a failure only marks that line (unavailable) and leaves the other
// lines alone; only **every probe failing** counts as a total failure (that means the sandbox cannot start at
// all, not that some capability is missing).
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
		// Concurrent: under docker each one is a cold container start (300ms~1s), and serially that would drag
		// startup out to several seconds
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
		// Total failure: the sandbox is unusable. No output can be trusted then, so better to give none.
		env.Error = fmt.Sprintf("all %d probes failed: %v", len(outcomes), outcomes[0].err)
		return env
	}
	// No trailing newline: this Text is pasted verbatim by nexus as one item of the system prompt, and an extra
	// \n would open a blank line between it and the next explanation (pure noise).
	env.Text = strings.TrimRight(b.String(), "\n")
	return env
}

// probeOutcome is the result of one probe (consumed in order by the summary).
type probeOutcome struct {
	label string
	text  string
	err   error
}

// sandboxProbes assembles the probes for this run: system information plus each language's self-report.
//
// Every probe's output goes into the agent's context verbatim, so line prefixes are written as plain English words
// (Python version / Installed Python packages / ...) rather than internal abbreviations: this text is model-facing
// only, and nexus keeps passing it through with zero parsing (see Environment.Text).
func (s *Server) sandboxProbes() []probe {
	// os: os-release is the de-facto standard (the base image is Debian, so it is always there); if it is
	// missing, runProbe reports unavailable — falling back to uname is left to a copy note on the nexus side,
	// and one more probe for an extreme environment is not worth it (the anomaly is information too).
	//
	// post only does layout: the whole os-release is a dozen lines, of which HOME_URL / SUPPORT_URL and the
	// like carry no information for the agent yet would ride along into the LLM context on every agent run.
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
		// The language entry name is resolved for the current mode: not defined in the top-level langs means
		// skip (that is "not configured", which is different from "configured but cannot run inside the
		// sandbox" and must not be reported as unavailable).
		cmd, err := s.cfg.ResolveLangCommand(lang)
		if err != nil {
			continue
		}
		probes = append(probes, probe{label: lang, argv: append([]string{cmd}, args...)})
	}

	// There used to be a luarocks probe here; it was removed:
	//   - it **always** fails on a read-only rootfs (luarocks writes a temp file first and reports
	//     "unable to generate a unique filename"), so it was forever a single unavailable line;
	//   - the failure reason is a 500-byte Lua stack trace that adds nothing about this line's usability and
	//     just eats LLM context;
	//   - the image ships no rocks at all, so the answer to "which third-party modules does Lua have" is
	//     "none", and one more probe to confirm it is not worth it (the lua probe no longer reports the search
	//     path either, see luaSelfReport).
	return probes
}

// runProbe runs one probe.
//
// It mounts nothing: a probe only reads system information, needs no workspace, and therefore never touches
// allowed_mount_roots.
// It goes through s.exec (the isolation implementation) rather than running on the host — the environment it
// probes is the one the agent actually gets (read-only rootfs / no network / --user all apply), otherwise it
// would be a different environment.
func (s *Server) runProbe(ctx context.Context, p probe) (string, error) {
	res, err := sandbox.Run(ctx, s.exec, &sandbox.Spec{
		Argv:   p.argv,
		Limits: sandbox.Limits{MaxOutputBytes: s.cfg.Probe.MaxOutputBytes},
	})
	if err != nil {
		return "", err
	}
	// A non-zero exit code is not an error (see the sandbox.Run contract) and must be checked here: a missing
	// command (127) and a missing file (1) both land here.
	if res.ExitCode != 0 {
		// Only the first line of the error: tool errors often carry a multi-line stack trace, and this text
		// goes into the LLM context — it would break the "one fact per line" shape without adding information
		// (the first line already states the cause).
		return "", fmt.Errorf("exit %d: %s", res.ExitCode, firstLine(string(res.Stderr)))
	}
	return string(res.Stdout), nil
}

// probeHost probes the host natively (mode=none: the sandbox is the host itself).
//
// Why not spawn uname / cat: the host may be Windows (local debugging runs this very path), where there is no
// sh / cat / uname — Go's own GOOS/GOARCH and LookPath are cross-platform and need no dependency.
//
// The output keeps the same "one fact per line" shape as the sandbox branch, so nexus does not have to tell the
// sources apart.
//
// The line prefixes are English and one of them is a cross-process protocol string: the nexus side parses the
// `OS: ` line to infer the platform (see sandboxinfo.OS), so changing it means changing both sides. An older
// sandboxd wrote that label in Chinese, and nexus still accepts the old spelling - otherwise a nexus upgraded
// before sandboxd would fail every command with "cannot determine the sandbox OS".
func (s *Server) probeHost() string {
	var b strings.Builder
	fmt.Fprintf(&b, "OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	for _, lang := range probeLangOrder {
		cmd, err := s.cfg.ResolveLangCommand(lang)
		if err != nil {
			continue
		}
		// The line prefix keeps the same plain style as the sandbox branch; the language name stays the
		// key from langs (that is what the user sees in the config) while the value is the absolute host path —
		// under mode=none that is the path the agent really uses.
		if p, lerr := exec.LookPath(cmd); lerr == nil {
			fmt.Fprintf(&b, "%s interpreter: %s\n", lang, filepath.ToSlash(p))
		} else {
			// Under mode=none the startup checkCommands already verified this; this is only defensive.
			fmt.Fprintf(&b, "%s interpreter: (unavailable: %s not found on the host PATH)\n", lang, cmd)
		}
	}
	return b.String()
}

// formatOSRelease tidies /etc/os-release into one "OS: <name> <version>" line.
//
// Layout only: it takes PRETTY_NAME / ID / VERSION_ID per the os-release format and drops HOME_URL /
// SUPPORT_URL / BUG_REPORT_URL and the like, which carry nothing for the agent yet would enter the LLM context
// on every run. It infers no conclusions (such as "does this system have yum") — that is the agent's own job,
// and stating it would mean deciding on its behalf.
//
// When none of the known keys is recognized it returns the input **unchanged**: better a raw os-release than
// silently swallowing facts.
func formatOSRelease(text string) string {
	var pretty, id, ver string
	for _, line := range strings.Split(text, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"`) // most os-release values are quoted; stripping reads more like a name
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
		return "OS: " + pretty
	case id != "" && ver != "":
		return "OS: " + id + " " + ver
	case id != "":
		return "OS: " + id
	default:
		return strings.TrimRight(text, "\n")
	}
}

// firstLine takes the first line and caps its length.
//
// It has exactly one use: squeezing "why this probe produced nothing" into one line — tool errors often carry
// multi-line stack traces (luarocks is the classic), and pasting all of it into Environment.Text would break the
// "one fact per line" shape without adding information.
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
