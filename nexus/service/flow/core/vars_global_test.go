package flow

import (
	"strings"
	"testing"
)

// 环境变量的 type: "global" 已下线（全局变量改成节点输入的来源）。
// 老数据仍可能带着它：必须报一句能读懂的话，而不是静默当普通字符串或返回空值。
func TestGetEnvVarRejectsDeprecatedGlobalType(t *testing.T) {
	envVars := map[string]interface{}{
		"API_TOKEN": map[string]interface{}{"type": "global", "value": "CRM_TOKEN"},
		"INLINE":    map[string]interface{}{"type": "string", "value": "hello"},
	}

	t.Run("global 类型报废弃错误", func(t *testing.T) {
		_, err := GetEnvVar(envVars, "API_TOKEN")
		if err == nil {
			t.Fatal("期望报错，实际返回成功")
		}
		if !strings.Contains(err.Error(), "已废弃") {
			t.Fatalf("错误信息应说明类型已废弃，实际: %v", err)
		}
	})

	t.Run("内联值不受影响", func(t *testing.T) {
		v, err := GetEnvVar(envVars, "INLINE")
		if err != nil {
			t.Fatalf("内联值不该报错: %v", err)
		}
		if v != "hello" {
			t.Fatalf("取值错误: %v", v)
		}
	})

	t.Run("路径不存在时报错", func(t *testing.T) {
		if _, err := GetEnvVar(envVars, "MISSING"); err == nil {
			t.Fatal("期望报错，实际返回成功")
		}
	})
}

// 全局变量作为输入来源（source_type: "global"）：这两条不用查库就能判，必须先拦住。
func TestResolveGlobalSourceGuards(t *testing.T) {
	t.Run("没填变量名时报错", func(t *testing.T) {
		_, err := ResolveGlobalSource("t0", "  ")
		if err == nil {
			t.Fatal("期望报错，实际返回成功")
		}
		if !strings.Contains(err.Error(), "没有填写全局变量名") {
			t.Fatalf("错误信息应指出缺少变量名，实际: %v", err)
		}
	})

	t.Run("没有租户上下文时报错", func(t *testing.T) {
		_, err := ResolveGlobalSource("", "DB_HOST")
		if err == nil {
			t.Fatal("期望报错，实际返回成功")
		}
		if !strings.Contains(err.Error(), "没有租户 schema") {
			t.Fatalf("错误信息应说明缺少租户上下文，实际: %v", err)
		}
	})
}

// 已废弃的 global 类型运行期不可赋值：报错必须指向迁移动作，原配置也不能被改坏。
func TestSetEnvVarRejectsDeprecatedGlobalType(t *testing.T) {
	envVars := map[string]interface{}{
		"API_TOKEN": map[string]interface{}{"type": "global", "value": "CRM_TOKEN"},
	}
	err := SetEnvVar(envVars, "API_TOKEN", "literal")
	if err == nil {
		t.Fatal("给已废弃的 global 类型赋值应被拒绝")
	}
	if !strings.Contains(err.Error(), "已废弃") {
		t.Fatalf("错误信息应说明类型已废弃，实际: %v", err)
	}
	item := envVars["API_TOKEN"].(map[string]interface{})
	if item["value"] != "CRM_TOKEN" || item["type"] != "global" {
		t.Fatalf("原配置被改动: %v", item)
	}
}
