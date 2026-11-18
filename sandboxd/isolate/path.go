package isolate

import (
	"path"
	"path/filepath"
	"strings"

	"flame/tools/sandbox"
)

// 路径约定：
//
//	Spec.Argv / Spec.Dir 中出现的路径统一使用「沙箱内」语义（如 exec 约定的 /workspace、
//	/agent，worker 执行器脚本的 /workers/default/worker.lua）。隔离实现（nsjail / docker）
//	按 Mounts 做挂载后即可直接使用；none 实现不做隔离，因此需要按 Mounts 反向翻译回宿主
//	路径再执行。
//
// 这样调用方无需区分实现，只写一套路径。

// TranslateToHost 把「沙箱内路径」按 mounts 反向翻译为宿主路径。
// 若没有任何挂载点匹配（例如可执行文件名 "lua54"、或本身已是宿主路径），
// 则原样返回。
func TranslateToHost(mounts []sandbox.Mount, guest string) string {
	if guest == "" || len(mounts) == 0 {
		return guest
	}

	var best *sandbox.Mount
	for i := range mounts {
		m := &mounts[i]
		if m.Guest == "" {
			continue
		}
		if !hasPathPrefix(guest, m.Guest) {
			continue
		}
		if best == nil || len(m.Guest) > len(best.Guest) {
			best = m
		}
	}
	if best == nil {
		return guest
	}

	rest := strings.TrimPrefix(path.Clean(filepath.ToSlash(guest)),
		path.Clean(filepath.ToSlash(best.Guest)))
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return best.Host
	}
	return filepath.Join(best.Host, filepath.FromSlash(rest))
}

// TranslateArgv 翻译 argv 中的沙箱内路径。
// 只翻译「以 / 开头」的参数（沙箱内绝对路径）；命令行字符串、开关
// （如 -c、echo hello）等一律跳过，避免误伤。第一个元素（可执行文件）
// 同样按此规则处理：像 "/usr/bin/lua5.4" 会被翻译，而 "lua5.4" 保持原样。
func TranslateArgv(mounts []sandbox.Mount, argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	copy(out, argv)
	for i, a := range out {
		if strings.HasPrefix(a, "/") {
			out[i] = TranslateToHost(mounts, a)
		}
	}
	return out
}

// hasPathPrefix 判断 p 是否位于 prefix 之下（按路径分段比较，避免 /work 匹配到 /workshop）。
func hasPathPrefix(p, prefix string) bool {
	pp := path.Clean(filepath.ToSlash(p))
	pre := path.Clean(filepath.ToSlash(prefix))
	if pre == "/" {
		return strings.HasPrefix(pp, "/")
	}
	return pp == pre || strings.HasPrefix(pp, pre+"/")
}
