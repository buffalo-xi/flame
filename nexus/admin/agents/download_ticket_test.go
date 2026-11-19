package agents

import (
	"testing"
	"time"

	"flame/nexus/shared/config"
)

// A ticket must only be honoured unchanged and unexpired: a different path, a different session or an old ticket must all fail.
func TestDownloadTicket(t *testing.T) {
	// The signing key comes from the global config: build a minimal one here.
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
		t.Fatal("a valid ticket was rejected")
	}

	tampered := tk
	tampered.Path = "uploads/b.md"
	if ticketValid(tampered, sig) {
		t.Error("a changed path still verified")
	}

	otherSession := tk
	otherSession.SessionID = "s2"
	if ticketValid(otherSession, sig) {
		t.Error("a different session still verified")
	}

	expired := tk
	expired.Exp = time.Now().Add(-time.Second).Unix()
	if ticketValid(expired, ticketSignature(expired)) {
		t.Error("an expired ticket still passed")
	}

	if ticketValid(tk, "") {
		t.Error("an empty signature still passed")
	}

	// The signed string must change with the fields (so a wrong concatenation order cannot collide).
	if ticketSignature(tk) == ticketSignature(tampered) {
		t.Error("the payload does not include path")
	}
}

// accelPath escapes segment by segment, never encodes the "/", and percent-encodes a non-ASCII name.
func TestAccelPath(t *testing.T) {
	got := accelPath("/__files", "/tenants/t0/users/u1/sessions/s1/uploads/报表 2026.md")
	want := "/__files/tenants/t0/users/u1/sessions/s1/uploads/%E6%8A%A5%E8%A1%A8%202026.md"
	if got != want {
		t.Errorf("accelPath = %q, want %q", got, want)
	}

	if got := accelPath("/__files/", "a/b.txt"); got != "/__files/a/b.txt" {
		t.Errorf("the trailing slash on the prefix was not normalised: %q", got)
	}
}
