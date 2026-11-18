package agents

import (
	"testing"
	"time"

	"flame/nexus/shared/config"
)

// 票据只该被「原样 + 未过期」地使用：改路径、改会话、过期都必须验不过。
func TestDownloadTicket(t *testing.T) {
	// 票据密钥取自全局配置：测试里造一个最小实例
	config.Cfg = &config.Config{JWT: config.JWTConfig{SecretKey: "test-secret-key-for-download-ticket-0123456789"}}

	tk := DownloadTicket{
		Schema:    "t0",
		AgentID:   "a1",
		SessionID: "s1",
		Path:      "uploads/a.md",
		Exp:       time.Now().Add(time.Minute).Unix(),
	}
	sig := ticketSignature(tk)
	if !ticketValid(tk, sig) {
		t.Fatal("合法票据被拒")
	}

	tampered := tk
	tampered.Path = "uploads/b.md"
	if ticketValid(tampered, sig) {
		t.Error("改了 path 仍然验签通过")
	}

	otherSession := tk
	otherSession.SessionID = "s2"
	if ticketValid(otherSession, sig) {
		t.Error("换了会话仍然验签通过")
	}

	expired := tk
	expired.Exp = time.Now().Add(-time.Second).Unix()
	if ticketValid(expired, ticketSignature(expired)) {
		t.Error("过期票据仍然通过")
	}

	if ticketValid(tk, "") {
		t.Error("空签名仍然通过")
	}

	// 签名字符串必须随字段变化（防止 payload 拼接顺序写错导致碰撞）
	if ticketSignature(tk) == ticketSignature(tampered) {
		t.Error("payload 未包含 path")
	}
}

// accelPath：逐段转义，不能把 "/" 编码掉；中文名要 percent-encode。
func TestAccelPath(t *testing.T) {
	got := accelPath("/__files", "/tenants/t0/users/u1/sessions/s1/uploads/报表 2026.md")
	want := "/__files/tenants/t0/users/u1/sessions/s1/uploads/%E6%8A%A5%E8%A1%A8%202026.md"
	if got != want {
		t.Errorf("accelPath = %q, want %q", got, want)
	}

	if got := accelPath("/__files/", "a/b.txt"); got != "/__files/a/b.txt" {
		t.Errorf("前缀尾部斜杠未归一: %q", got)
	}
}
