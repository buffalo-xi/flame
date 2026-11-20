package browserhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Run processes JSON-line requests from stdin and writes the results back to stdout, until EOF.
//
// EOF means the upper layer closed stdin (the session ended / the process was killed and the channel broke).
// At that point the browser must be torn down as well: Chrome is a child of this process, and once this process
// exits it would be orphaned and keep holding memory until the container is destroyed.
func Run(stdin io.Reader, stdout, stderr io.Writer, cfg Config) error {
	cfg.Logf = func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "browserhost: "+format+"\n", args...)
	}
	host := New(cfg)
	defer host.Close()

	out := &lineWriter{w: bufio.NewWriter(stdout)}
	sc := bufio.NewScanner(stdin)
	// The theoretical maximum of a single command: URL + typed text + selector. 4MB is allowed; anything beyond
	// that is treated as a protocol error rather than silently truncated into half a command and executed.
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	for sc.Scan() {
		// Tolerance: some Windows tools feed data through a pipe with a UTF-8 BOM. The normal path (forwarding by
		// sandboxd) never has one, but stripping it once more costs nothing.
		line := bytes.TrimSpace(bytes.TrimPrefix(sc.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = out.write(&Response{Code: "bad_request", Error: "invalid json: " + err.Error()})
			continue
		}
		resp := host.Handle(req)
		if err := out.write(&resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// SelfTest is the self-check: it only confirms "the browser executable exists and can report a version", it never
// actually launches the browser.
//
// Why a separate command: sandboxd needs to tell nexus honestly "does this image contain a browser at all" without
// paying the cost of starting a Chrome.
func SelfTest(execPath string) (string, error) {
	path := execPath
	if path == "" {
		var err error
		path, err = DetectExecPath()
		if err != nil {
			return "", err
		}
	}
	base := strings.ToLower(filepath.Base(path))

	// msedge.exe does not recognise --version: it treats it as a launch, prints "opening in an existing browser
	// session" and may really pop a window. So it is not called at all and only the engine name is reported — a
	// missing version number is not "the browser is unavailable" and must not fail the capability probe.
	if strings.HasPrefix(base, "msedge") {
		return "Microsoft Edge (version unknown)", nil
	}

	// On Windows nothing is spawned: chrome.exe does not recognise --version either (it treats it as a launch and
	// hands it to an existing session, and the observed output is a GBK "opening in an existing browser session"),
	// and that output would be dropped by isPrintableASCII anyway, still ending up at "version unknown". Since the
	// result is fixed, it is not worth paying for a process start **without any timeout**:
	//   - when the existing session is busy this hand-off blocks for a long time -> burns sandboxd's 30s probe
	//     timeout -> the probe reads an empty stdout -> reports `bad output ""`, with the real cause (the block)
	//     still invisible.
	if runtime.GOOS == "windows" {
		return engineLabel(base) + " (version unknown)", nil
	}

	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		// Same for other builds that do not report a version: finding the executable is enough, the version is
		// only extra information.
		return engineLabel(base) + " (version unknown)", nil
	}
	line := firstLine(out)
	if !isPrintableASCII(line) {
		return engineLabel(base) + " (version unknown)", nil
	}
	return line, nil
}

func firstLine(out []byte) string {
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// isPrintableASCII filters out non-ASCII output: browser error messages on Windows are often Chinese in GBK
// encoding (for example Edge's "opening in an existing browser session"), and pasting that into the prompt verbatim
// would turn into mojibake.
func isPrintableASCII(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return strings.ContainsAny(s, "0123456789")
}

func engineLabel(base string) string {
	switch {
	case strings.HasPrefix(base, "chrome"):
		return "Google Chrome"
	case strings.HasPrefix(base, "chromium"):
		return "Chromium"
	case strings.HasPrefix(base, "msedge"):
		return "Microsoft Edge"
	default:
		return "Chromium"
	}
}

// DetectExecPath discovers the browser executable inside the sandbox.
//
// The order is deliberate: chromium first (it is what the docker image installs, distribution-maintained, no EULA),
// then chrome / google-chrome, and finally Edge on Windows (the most likely one already installed for local
// development).
func DetectExecPath() (string, error) {
	names := []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"}
	if runtime.GOOS == "windows" {
		names = []string{"chrome", "chromium", "msedge"}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	candidates := windowsBrowserPaths()
	if runtime.GOOS != "windows" {
		candidates = append(candidates,
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("no browser found: install chromium, or set browser.exec_path")
}

func windowsBrowserPaths() []string {
	var roots []string
	for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
		if v := os.Getenv(env); v != "" {
			roots = append(roots, v)
		}
	}
	rel := []string{
		filepath.Join("Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join("Chromium", "Application", "chrome.exe"),
		filepath.Join("Microsoft", "Edge", "Application", "msedge.exe"),
	}
	var out []string
	for _, root := range roots {
		for _, r := range rel {
			out = append(out, filepath.Join(root, r))
		}
	}
	return out
}
