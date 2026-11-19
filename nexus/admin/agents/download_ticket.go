package agents

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sessions"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 工作区文件下载票
//
// 浏览器的「下载」是导航行为（window.location），带不了 Authorization 头。
// 让 JWT 进 URL 会留在 nginx access log / 浏览器历史 / Referer 里，所以这里换一张
// 短时（60s）、绑定 tenant + agent + session + path 的 HMAC 票：
//   - 无状态，不落库，多实例部署天然一致；
//   - 签名密钥复用 jwt.secret_key（多实例必须一致，否则验签随机失败）。
// ============================================================================

const downloadTicketTTL = 60 * time.Second

// DownloadTicket 票据内容：签进 HMAC 的就是这几个字段，改任何一个都会验签失败。
type DownloadTicket struct {
	Schema    string
	AgentID   string
	SessionID string
	Path      string
	Exp       int64
}

func ticketPayload(t DownloadTicket) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", t.Schema, t.AgentID, t.SessionID, t.Path, t.Exp)
}

func ticketSignature(t DownloadTicket) string {
	mac := hmac.New(sha256.New, []byte(config.Cfg.JWT.SecretKey))
	mac.Write([]byte(ticketPayload(t)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func ticketValid(t DownloadTicket, sig string) bool {
	if t.Schema == "" || t.AgentID == "" || t.SessionID == "" || t.Path == "" || sig == "" {
		return false
	}
	if t.Exp < time.Now().Unix() {
		return false
	}
	return hmac.Equal([]byte(ticketSignature(t)), []byte(sig))
}

// CreateDownloadTicket 给「下载」按钮签票（需要登录态）。
// 返回 schema / path / exp / sig，前端自己拼成 URL 再 window.location 导航。
func CreateDownloadTicket(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	var body struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
	}
	if berr := c.ShouldBindJSON(&body); berr != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 签票是登录态动作，按「会话归属用户」收口：
	// 票据一旦签出，/filedl 那条无认证的路就只认签名了。
	sess, lerr := lookupSessionForUser(info, info.Schema, body.SessionID)
	if lerr != nil {
		respondSessionErr(c, lerr, notOwnedSession)
		return
	}
	// 会话目录按 user 组织，没有归属用户就没有可签的路径
	if sess.UserID == "" {
		xhttp.ErrJSON(c, notOwnedSession, nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	wsKey := paths.UserSessionKey(info.Schema, sess.UserID, sess.ID)
	abs, clean, jerr := joinKey(wsKey, body.Path)
	if jerr != nil || clean == "" {
		xhttp.ErrJSON(c, "invalid path", nil, "")
		return
	}
	// 签票前先确认文件存在：否则用户拿到票、点了才发现 404
	if _, _, statErr := st.Stat(context.Background(), abs); statErr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}

	t := DownloadTicket{
		Schema:    info.Schema,
		AgentID:   inst.ID,
		SessionID: sess.ID,
		Path:      clean,
		Exp:       time.Now().Add(downloadTicketTTL).Unix(),
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"schema": t.Schema,
		"path":   t.Path,
		"exp":    t.Exp,
		"sig":    ticketSignature(t),
	}, "")
}

// DownloadByTicket 导航式下载入口：没有 Authorization 头，靠 URL 里的签名鉴权。
// 路由必须注册在认证中间件之前（见 admin.SetupRoutes）。
func DownloadByTicket(c *gin.Context) {
	exp, _ := strconv.ParseInt(c.Query("exp"), 10, 64)
	t := DownloadTicket{
		Schema:    c.Query("schema"),
		AgentID:   c.Param("id"),
		SessionID: c.Query("session_id"),
		Path:      c.Query("path"),
		Exp:       exp,
	}
	if !ticketValid(t, c.Query("sig")) {
		c.String(http.StatusForbidden, "invalid or expired download ticket")
		return
	}

	// 票据里带了 schema：伪造 AuthInfo 让仓储层照常按租户查（没有登录态可用）
	inst, err := repo.GetAgent(&auth.AuthInfo{Schema: t.Schema}, t.AgentID)
	if err != nil || inst == nil {
		c.String(http.StatusNotFound, "agent not found")
		return
	}
	sess, lerr := sessions.NewRepository().Lookup(
		t.Schema, t.SessionID, models.SessionOwnerTypeAgent, inst.ID,
	)
	if lerr != nil || sess.UserID == "" {
		c.String(http.StatusNotFound, "session not found")
		return
	}
	st, serr := getStorage()
	if serr != nil {
		c.String(http.StatusInternalServerError, "storage not ready")
		return
	}
	wsKey := paths.UserSessionKey(t.Schema, sess.UserID, sess.ID)
	abs, clean, jerr := joinKey(wsKey, t.Path)
	if jerr != nil || clean != t.Path { // clean != 票据里的 path：说明路径被规整过，拒绝
		c.String(http.StatusBadRequest, "invalid path")
		return
	}
	ServeDownload(c, st, abs, clean)
}
