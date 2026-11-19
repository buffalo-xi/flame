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

// The dropdown order is part of the API: the front end takes scriptLanguages[0] as the default for a new node,
// so lua must always come first (it is the high-frequency microsecond language), and languages the sandbox adds must not be swallowed.
func TestOrderLangs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"sandbox order -> lua first", []string{"js", "lua", "python"}, []string{"lua", "python", "js"}},
		{"python only (do not force lua in)", []string{"python"}, []string{"python"}},
		{"unknown languages appended in order", []string{"ruby", "js", "lua", "go"}, []string{"lua", "js", "go", "ruby"}},
		{"empty list", []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderLangs(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("orderLangs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Language choice belongs to the sandbox: with a snapshot it returns the list (re-ordered for display),
// without one it returns an empty list (the front end falls back to ["lua"]); there is no local list any more.
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
		t.Errorf("the sandbox list should be re-ordered with lua first, got %v", okResp.Data)
	}

	// No sandbox snapshot: return an empty list instead of a local fallback.
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
		t.Errorf("should return an empty list without the sandbox, got %v", emptyResp.Data)
	}
}
