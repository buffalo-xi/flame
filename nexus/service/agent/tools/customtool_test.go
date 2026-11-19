package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
)

// 出参校验只做浅层：顶层 required + 基础类型。
// 断言的重点不是"能拦多少"，而是**该放行的必须放行** —— 多返回字段是常态（前向兼容），
// 把它判成错误会让每个脚本作者被迫追着 schema 改。
func TestValidateScriptOutput(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"rows"},
		"properties": map[string]any{
			"rows":  map[string]any{"type": "array"},
			"total": map[string]any{"type": "number"},
			"ok":    map[string]any{"type": "boolean"},
		},
	}

	cases := []struct {
		name    string
		result  any
		wantErr string
	}{
		{"合法", map[string]any{"rows": []any{}, "total": float64(3), "ok": true}, ""},
		{"缺少必填", map[string]any{"total": float64(1)}, "missing fields"},
		{"类型不符", map[string]any{"rows": []any{}, "total": "3"}, "wrong type"},
		{"多返回字段放行", map[string]any{"rows": []any{}, "extra": "x"}, ""},
		{"整体不是对象", []any{1, 2}, "must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateScriptOutput("demo", schema, tc.result)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应通过，实际 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("应报 %q，实际 %v", tc.wantErr, err)
			}
		})
	}
}

// 密钥遮蔽：只遮长度 ≥ 8 的值。
// 短值（如 "cn"）会误伤正常文本，规则必须写死并被测试守住。
func TestMaskSecrets(t *testing.T) {
	cfg := map[string]any{"api_key": "sk-1234567890", "region": "cn", "n": 42}

	out, masked := maskSecrets(`{"echo":"sk-1234567890","region":"cn"}`, cfg)
	if !masked {
		t.Fatal("应报告已遮蔽")
	}
	if strings.Contains(out, "sk-1234567890") {
		t.Errorf("密钥未被遮蔽: %s", out)
	}
	if !strings.Contains(out, `"cn"`) {
		t.Errorf("短值不应被误伤: %s", out)
	}

	if _, m := maskSecrets(`{"x":1}`, nil); m {
		t.Error("没有配置时不该报告遮蔽")
	}
}

// 工具名前缀规则：custom__ 是命名空间，去掉前缀才是 DB 里的短名。
func TestCustomToolNamePrefix(t *testing.T) {
	if !IsCustomToolName(CustomToolPrefix + "sum_sales") {
		t.Error("custom__ 前缀应被识别")
	}
	if IsCustomToolName("read_file") || IsCustomToolName("web_search") {
		t.Error("内置 / 插件工具不该被识别为自定义工具")
	}
	if got := CustomShortName(CustomToolPrefix + "sum_sales"); got != "sum_sales" {
		t.Errorf("短名 = %q", got)
	}
}

// 能力档必须是 exec：自定义工具走 sb.Start → /v1/exec（一次性进程、无池），
// 与 run_skill_script 同一条通道。判据是**通道**，不是「跑的是不是脚本文件」。
//
// 钉住这条是为了防回退：归到 sandbox.code（= /v1/code 通道档）会造成
// 「关工作流 code 节点把租户的自定义工具一起关掉」，而那种失效**没有报错**，只有功能消失。
func TestCustomToolRequiresExecCapability(t *testing.T) {
	c := &Context{
		CustomTools: &stubCustomToolSource{spec: &CustomToolSpec{Name: "demo", Lang: "sh", Entry: "main.sh"}},
		// 只开代码片段档：不该放行
		Caps: map[string]bool{capability.Code: true},
	}

	_, err := executeCustomTool(context.Background(), c, CustomToolPrefix+"demo", nil)
	if err == nil || !strings.Contains(err.Error(), capability.Exec) {
		t.Fatalf("未开通 exec 时应拒绝并指出 %s，got %v", capability.Exec, err)
	}

	// 开通 exec 后应越过能力闸，停在下一道检查（根目录未初始化）
	c.Caps = map[string]bool{capability.Exec: true}
	_, err = executeCustomTool(context.Background(), c, CustomToolPrefix+"demo", nil)
	if err == nil || !strings.Contains(err.Error(), "root is not initialized") {
		t.Fatalf("越过能力闸后应停在根目录检查，got %v", err)
	}

	// Caps == nil 只用于无租户语义的调用方（测试/内部），不拦
	c.Caps = nil
	if _, err := executeCustomTool(context.Background(), c, CustomToolPrefix+"demo", nil); err != nil &&
		strings.Contains(err.Error(), "能力") {
		t.Fatalf("Caps 为 nil 时不该按能力拦，got %v", err)
	}
}

type stubCustomToolSource struct{ spec *CustomToolSpec }

func (s *stubCustomToolSource) List(context.Context) ([]CustomToolSpec, error) { return nil, nil }

func (s *stubCustomToolSource) Get(context.Context, string) (*CustomToolSpec, error) {
	return s.spec, nil
}

func (s *stubCustomToolSource) Status(context.Context, string) (int, bool, error) {
	return 1, true, nil
}
