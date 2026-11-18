package plugins

import (
	"strings"
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// Upsert 必须拒绝空 kind。
//
// DB 的 DEFAULT 'tools' 只在**不显式传该列**时生效；一旦把空串写进 kind 列，
// 那一行就再也匹配不上任何按 kind 过滤的查询 —— 表现是「保存成功，但插件凭空消失」，
// 属于最难查的一类故障。这条校验发生在碰 DB 之前，所以不需要连库就能测。
func TestUpsertRejectsEmptyKind(t *testing.T) {
	err := NewRepository().Upsert(&auth.AuthInfo{Schema: "t0"}, &models.Plugin{
		Kind: "   ",
		Name: "web_search",
	})
	if err == nil {
		t.Fatal("空 kind 应当报错，实际返回 nil（会落库成 kind=''）")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("错误信息应提到 kind 便于定位，实际：%v", err)
	}
}

func TestUpsertRejectsNilPlugin(t *testing.T) {
	if err := NewRepository().Upsert(&auth.AuthInfo{Schema: "t0"}, nil); err == nil {
		t.Fatal("nil plugin 应当报错")
	}
}
