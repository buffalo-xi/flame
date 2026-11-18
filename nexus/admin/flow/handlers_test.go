package flow

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"flame/nexus/shared/sandboxinfo"
	"github.com/gin-gonic/gin"
)

// 下拉顺序是接口的一部分：前端拿 scriptLanguages[0] 当新建节点的缺省语言，
// 所以 lua 必须始终打头（它是 µs 级高频语言），沙箱新加的语言也不能被吞掉。
func TestOrderLangs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"沙箱的字典序 → lua 打头", []string{"js", "lua", "python"}, []string{"lua", "python", "js"}},
		{"只有 python（lua 不在时不该硬塞）", []string{"python"}, []string{"python"}},
		{"未知语言按字典序追加在尾部", []string{"ruby", "js", "lua", "go"}, []string{"lua", "js", "go", "ruby"}},
		{"空列表", []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderLangs(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("orderLangs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// 语言选择完全由沙箱决定：有快照时返回（按展示顺序重排），没有时返回空列表
// （前端保底成 ["lua"]），不再有本地兜底名单。
func TestGetCodeLanguages(t *testing.T) {
	old := sandboxinfo.Get()
	t.Cleanup(func() { sandboxinfo.Set(old) })

	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		CodeLangs: []string{"js", "lua", "python"},
		FetchedAt: time.Now(),
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/flow/script-languages", nil)
	GetCodeLanguages(c)
	var okResp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("unmarshal ok resp: %v", err)
	}
	if !reflect.DeepEqual(okResp.Data, []string{"lua", "python", "js"}) {
		t.Errorf("沙箱上报应重排为 lua 打头, got %v", okResp.Data)
	}

	// 拿不到沙箱快照：返回空列表，而非本地兜底。
	sandboxinfo.Set(sandboxinfo.Info{Err: "dial tcp 127.0.0.1:27081: connect: connection refused"})
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest("GET", "/admin/flow/script-languages", nil)
	GetCodeLanguages(c2)
	var emptyResp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &emptyResp); err != nil {
		t.Fatalf("unmarshal empty resp: %v", err)
	}
	if len(emptyResp.Data) != 0 {
		t.Errorf("拿不到沙箱时应返回空列表, got %v", emptyResp.Data)
	}
}
