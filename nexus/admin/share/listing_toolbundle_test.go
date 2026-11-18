package share

import "testing"

// 打包时「随包带走的自定义工具」只认 custom:<名字>。
//
// 这条判据错一次会出现两种完全不同的症状，两种都真实发生过：
//   - plugin:web_search 被当成目录名 → Windows 上冒号非法文件名 → 整个上架失败；
//   - custom:web_search 连前缀一起当目录名 → tools/custom:web_search 不存在 →
//     读契约拿到 ErrNoRows 后静默跳过 → 自定义工具一个都没被带走。
func TestCustomToolNameOfRef(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"custom:web_search", "web_search"},
		{"custom:Weather-2", "Weather-2"},
		{"  custom:weather  ", "weather"}, // ParseRef 会 TrimSpace
		{"plugin:web_search", ""},         // 插件由磁盘实现提供，没有本体
		{"plugin:web_search:extra", ""},   // 插件不带第三段
		{"builtin:read_file", ""},
		{"builtin:file", ""},
		{"group:ops", ""},
		{"preset:agent-manage", ""},
		{"mcp:fs:read_file", ""},
		{"rpc:planner:run", ""},
		{"local:rpc:poet", ""},
		{"", ""},
		{"custom:", ""},        // 缺 subject
		{"custom:../evil", ""}, // subject 含路径分隔符
		{"custom:..", ""},      // subject 不能以 . 开头
		{"nonsense", ""},       // 没有 scope
		{"weather", ""},        // 历史遗留裸名不做兼容：拿不准就别打包
		{"weird:x:y:z", ""},    // 段数超限
	}
	for _, c := range cases {
		if got := customToolNameOfRef(c.ref); got != c.want {
			t.Errorf("customToolNameOfRef(%q) = %q, 期望 %q", c.ref, got, c.want)
		}
	}
}
