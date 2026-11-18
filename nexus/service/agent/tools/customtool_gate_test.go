package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/models"
)

// fakeScriptSource 只实现文件闸用到的那三个方法。
type fakeScriptSource struct {
	status int
	found  bool
}

func (f fakeScriptSource) List(context.Context) ([]CustomToolSpec, error) { return nil, nil }

func (f fakeScriptSource) Get(context.Context, string) (*CustomToolSpec, error) { return nil, nil }

func (f fakeScriptSource) Status(context.Context, string) (int, bool, error) {
	return f.status, f.found, nil
}

// tools/{name}/** 纳入文件闸后的四条判据。
//
// 与 skills 的差别：自定义工具的代码**就是**工具本体，所以「不存在」必须拒绝写 ——
// 否则可以直接 mkdir 一个目录，绕过 custom_tools 表造出「盘上有、库里没有」的孤儿。
func TestCheckWriteCustomTool(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		src     CustomToolSource
		path    string
		wantErr string
	}{
		{"超级智能体写正常态工具", "main", fakeScriptSource{status: models.StatusNormal, found: true}, "tools/sum/main.py", ""},
		{"工具不存在", "main", fakeScriptSource{}, "tools/sum/main.py", "不存在"},
		{"工具已锁定", "main", fakeScriptSource{status: models.StatusLocked, found: true}, "tools/sum/main.py", "锁定"},
		{"实体根目录本身", "main", fakeScriptSource{status: models.StatusNormal, found: true}, "tools/sum", "实体目录本身"},
		{"非 supervisor 一律拒绝", "poet", fakeScriptSource{status: models.StatusNormal, found: true}, "tools/sum/main.py", "仅 supervisor"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 身份判据是属性：测试里 "main" 表示超权 agent，须显式置位
			c := &Context{AgentName: tc.agent, IsSupervisor: tc.agent == "main", CustomTools: tc.src}
			err := checkWrite(context.Background(), c, tc.path)
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

// 未注入 CustomTools（单测 / 无租户语义的调用方）时不能 panic，要给出可读错误。
func TestCheckWriteCustomToolWithoutSource(t *testing.T) {
	c := &Context{AgentName: "main", IsSupervisor: true}
	err := checkWrite(context.Background(), c, "tools/sum/main.py")
	if err == nil || !strings.Contains(err.Error(), "script tool source") {
		t.Fatalf("应报 source 不可用，实际 %v", err)
	}
}
