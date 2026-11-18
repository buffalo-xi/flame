package daemon

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"flame/tools/sandbox"
)

// guardMounts 校验挂载表的 Host 是否全部落在允许的根之一之下。
//
// 背景：Mounts[].Host 是调用方给出的宿主绝对路径；不校验的话，一个伪造或写错的
// 请求就能把 /etc、/ 之类的目录挂进沙箱。这属于「策略」层面的兜底 ——
// sandboxd 不解析调用方的业务路径（那是调用方自己的责任），只做范围校验。
//
// roots 为空表示未配置白名单 —— 此时不校验（启动阶段会打警告）。
func guardMounts(mounts []sandbox.Mount, roots []string) error {
	if len(roots) == 0 {
		return nil
	}

	absRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return fmt.Errorf("invalid allowed_mount_roots entry %q: %w", r, err)
		}
		absRoots = append(absRoots, abs)
	}
	if len(absRoots) == 0 {
		return nil
	}

	for _, m := range mounts {
		if m.Host == "" {
			continue
		}
		abs, err := filepath.Abs(m.Host)
		if err != nil {
			return fmt.Errorf("invalid mount host %q: %w", m.Host, err)
		}

		matched := false
		for _, root := range absRoots {
			rel, rerr := filepath.Rel(root, abs)
			if rerr != nil {
				continue // 不同盘符等无法比较：这一条不匹配，继续试下一个 root
			}
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("mount %q is outside allowed_mount_roots %v", m.Host, roots)
		}
	}
	return nil
}

// guardDir 校验 dir 落在某个挂载点的 Guest 之下（空串表示交给实现默认）。
//
// 必要性：none 实现的 TranslateToHost 在匹配不到任何挂载点时会**原样返回输入**，
// 于是一个本该是「沙箱内路径」的 dir 会变成宿主路径被直接当成工作目录使用 ——
// 打破了「Spec 中的路径一律是沙箱内语义」这个契约。
//
// 注意 Guest 是沙箱内语义，因此这里用 path（正斜杠）而不是 filepath 来规范化。
func guardDir(dir string, mounts []sandbox.Mount) error {
	if dir == "" {
		return nil
	}
	d := path.Clean(filepath.ToSlash(dir))
	for _, m := range mounts {
		if m.Guest == "" {
			continue
		}
		g := path.Clean(filepath.ToSlash(m.Guest))
		if d == g || strings.HasPrefix(d, g+"/") {
			return nil
		}
	}
	return fmt.Errorf("dir %q is not under any mount guest", dir)
}
