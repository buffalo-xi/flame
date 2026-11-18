package flows

import (
	"encoding/json"
	"strings"
	"testing"
)

// validateDiagramLabels 是保存口的硬校验：画布提交的是 JSON 字符串（前端先 stringify 了一次），
// 工具写库的是对象，两种形态都要能认出重名。
func TestValidateDiagramLabels(t *testing.T) {
	cases := []struct {
		name    string
		diagram string
		wantErr string
	}{
		{
			name:    "全图唯一",
			diagram: `{"nodes":[{"id":"1","data":{"label":"开始"}},{"id":"2","data":{"label":"代码"}}]}`,
		},
		{
			name:    "对象形态重名",
			diagram: `{"nodes":[{"id":"2","data":{"label":"代码"}},{"id":"3","data":{"label":"代码"}}]}`,
			wantErr: "不能重复",
		},
		{
			name:    "画布提交的字符串形态重名",
			diagram: `"{\"nodes\":[{\"id\":\"2\",\"data\":{\"label\":\"代码\"}},{\"id\":\"3\",\"data\":{\"label\":\"代码\"}}]}"`,
			wantErr: "不能重复",
		},
		{
			name:    "空 label 不参与",
			diagram: `{"nodes":[{"id":"2","data":{}},{"id":"3","data":{}}]}`,
		},
		{
			name:    "空 diagram",
			diagram: ``,
		},
		{
			name:    "形状不认识时放行（不归这里管）",
			diagram: `[]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDiagramLabels(json.RawMessage(tc.diagram))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantErr)
			}
		})
	}
}
