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

// Run 处理 stdin 上的 JSON 行请求，把结果写回 stdout，直到 EOF。
//
// EOF = 上层关掉了 stdin（会话结束 / 进程被 kill 的通道断开）。
// 这时必须把浏览器一起收掉：Chrome 是本进程的子进程，本进程退出后
// 它会变成孤儿，一直占着内存直到容器销毁。
func Run(stdin io.Reader, stdout, stderr io.Writer, cfg Config) error {
	cfg.Logf = func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "browserhost: "+format+"\n", args...)
	}
	host := New(cfg)
	defer host.Close()

	out := &lineWriter{w: bufio.NewWriter(stdout)}
	sc := bufio.NewScanner(stdin)
	// 单条命令的理论上限：URL + 输入文本 + 选择器。给到 4MB，
	// 超过即视为协议错误（而不是悄悄截断成半条命令去执行）。
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	for sc.Scan() {
		// 容错：某些 Windows 工具经管道喂数据时会带 UTF-8 BOM。
		// 正常路径（sandboxd 转发）不会有，但多剥一次成本为零。
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

// SelfTest 自检：只确认「浏览器可执行文件存在且能报版本」，不真的拉起浏览器。
//
// 为什么单独一条：sandboxd 需要在不付「起一个 Chrome」代价的前提下，
// 如实告诉 nexus「这个镜像里到底有没有浏览器」。
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

	// msedge.exe 不认 --version：它会把这当成一次启动，输出一句
	// 「正在现有的浏览器会话中打开」并可能真的弹个窗口。所以直接不调它，
	// 只报引擎名 —— 版本号缺失不是「浏览器不可用」，不该让能力探测失败。
	if strings.HasPrefix(base, "msedge") {
		return "Microsoft Edge (version unknown)", nil
	}

	// Windows 上一律不 spawn：chrome.exe 同样不认 --version（它把这当成一次启动、
	// 转交给已有会话，实测输出是 GBK 的「正在现有的浏览器会话中打开。」），而那串
	// 输出必然被 isPrintableASCII 丢掉、最终仍落到「版本未知」。既然结果固定，
	// 就不值得为它付一次**不设超时**的进程启动：
	//   已有会话繁忙时这次转交会长时间阻塞 → 耗光 sandboxd 的 30s 探测超时 →
	//   探测读到空 stdout → 报 `bad output ""`，而真实原因（阻塞）还看不到。
	if runtime.GOOS == "windows" {
		return engineLabel(base) + " (version unknown)", nil
	}

	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		// 其他不报版本的构建同理：能探测到可执行文件就已足够，
		// 版本只是附加信息。
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

// isPrintableASCII 过滤掉非 ASCII 输出：Windows 上的浏览器错误信息常是
// GBK 编码的中文（例如 Edge 的「正在现有的浏览器会话中打开」），原样塞进
// 提示词会变成乱码。
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

// DetectExecPath 在沙箱内探测浏览器可执行文件。
//
// 顺序是刻意的：chromium 优先（docker 镜像里装的就是它，发行版维护、无 EULA），
// 其次 chrome / google-chrome，最后 Windows 上的 Edge（本机开发时最可能已装）。
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
