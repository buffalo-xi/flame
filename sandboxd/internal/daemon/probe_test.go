package daemon

import (
	"strings"
	"testing"
)

// formatOSRelease 只做排版：取已知键、丢掉噪声行，**不推断结论**。
// 这条界线是刻意的 —— 一旦这里开始「解释」（比如「这系统有没有 yum」），
// 就等于替 agent 下结论，而那本该由它自己从系统名推断。
func TestFormatOSRelease(t *testing.T) {
	// 真实样例（Debian 13 容器里的 /etc/os-release），也是当前触发这次改动的那个：
	// 十来行里只有 PRETTY_NAME 对 agent 有意义。
	debian := `PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
NAME="Debian GNU/Linux"
VERSION_ID="13"
VERSION="13 (trixie)"
VERSION_CODENAME=trixie
DEBIAN_VERSION_FULL=13.7
ID=debian
HOME_URL="https://www.debian.org/"
SUPPORT_URL="https://www.debian.org/support"
BUG_REPORT_URL="https://bugs.debian.org/"
`

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"真实样例 → 一行", debian, "系统: Debian GNU/Linux 13 (trixie)"},
		{"无引号的值", "PRETTY_NAME=Alpine Linux v3.20\nID=alpine\n", "系统: Alpine Linux v3.20"},
		{"缺 PRETTY_NAME → 退回 ID + 版本", "ID=alpine\nVERSION_ID=3.20\n", "系统: alpine 3.20"},
		{"只有 ID", "ID=debian\n", "系统: debian"},
		{
			// 认不出任何已知键：宁可原样给，也不要凭空吞掉事实。
			// os-release 缺失时 `cat` 会报错走 unavailable，这里防的是「文件在、但格式不像」。
			"认不出 → 原样返回",
			"some unexpected\ntext here\n",
			"some unexpected\ntext here",
		},
		{"空输入 → 原样（空串）", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatOSRelease(tc.in); got != tc.want {
				t.Errorf("formatOSRelease() = %q, want %q", got, tc.want)
			}
		})
	}
}

// firstLine：失败信息只留首行 + 设上限。
// 起因是 luarocks 那条探针在只读 rootfs 下必然失败，并把 500 字节的 Lua 栈回溯
// 灌进了 Environment.Text —— 破坏了「每行一条」的形状，也没有信息量。
func TestFirstLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"单行原样", "exit 1: permission denied\n", "exit 1: permission denied"},
		{"多行只取首行", "boom\nstack traceback:\n\t[C]: in function 'x'\n", "boom"},
		{"空输出给占位", "\n\n", "no output"},
		{"超长截断", strings.Repeat("a", 300), strings.Repeat("a", 200) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in); got != tc.want {
				t.Errorf("firstLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
