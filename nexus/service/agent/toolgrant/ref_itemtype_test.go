package toolgrant

import (
	"testing"

	"flame/nexus/shared/models"
)

// item_type 只能由 ref 推导：写入口不接受调用方传入。
// 推错的后果是「矛盾行」——item_type 与 ref 指向不同形态，前端勾选态与授权行对不上，
// 且不会报错（见 ItemTypeOfRef 的注释）。
func TestItemTypeOfRef(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		// 组形态
		{"group:fs-basic", models.GrantItemGroup},
		{"preset:default", models.GrantItemGroup},
		{"builtin:file", models.GrantItemGroup}, // 类别
		{"mcp:fs", models.GrantItemGroup},       // 整条连接
		{"rpc:erp", models.GrantItemGroup},
		{"local:rpc", models.GrantItemGroup},

		// 单工具形态
		{"builtin:read_file", models.GrantItemTool},
		{"mcp:fs:read_file", models.GrantItemTool},
		{"rpc:erp:query", models.GrantItemTool},
		{"local:rpc:flow-a", models.GrantItemTool},
		{"custom:web_search", models.GrantItemTool}, // 一工具一 ref
		{"plugin:web_search", models.GrantItemTool},

		// 非法 ref：保守按 tool（写路径此前已用 ValidateRef 拦下，这里只求不 panic）
		{"", models.GrantItemTool},
	}
	for _, c := range cases {
		if got := ItemTypeOfRef(c.ref); got != c.want {
			t.Errorf("ItemTypeOfRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
