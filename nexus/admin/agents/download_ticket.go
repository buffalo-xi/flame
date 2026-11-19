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
// Workspace file download ticket.
//
// A browser download is a navigation (window.location) and cannot carry an Authorization header.
// Putting the JWT in the URL would leave it in nginx access logs, browser history and Referer, so this
// uses a short-lived (60s) HMAC ticket bound to tenant + agent + session + path instead:
//   - stateless and never stored, so any number of instances agree by construction;
//   - the signing key reuses jwt.secret_key (it must match across instances, or verification fails at random).
// ============================================================================

const downloadTicketTTL = 60 * time.Second

// DownloadTicket is the ticket payload: exactly these fields are signed, so changing any one fails verification.
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

// CreateDownloadTicket signs a ticket for the download button (requires a session).
// It returns schema / path / exp / sig; the front end assembles the URL and navigates with window.location.
func CreateDownloadTicket(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the agent: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Signing is an authenticated action, scoped by the session's owning user:
	// once a ticket exists, the unauthenticated /filedl route trusts the signature alone.
	sess, lerr := lookupSessionForUser(info, info.Schema, body.SessionID)
	if lerr != nil {
		respondSessionErr(c, lerr, notOwnedSession)
		return
	}
	// Session directories are organised per user, so a session without an owner has no path to sign.
	if sess.UserID == "" {
		xhttp.ErrCode(c, "session.not_found", nil, notOwnedSession, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "agent.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	wsKey := paths.UserSessionKey(info.Schema, sess.UserID, sess.ID)
	abs, clean, jerr := joinKey(wsKey, body.Path)
	if jerr != nil || clean == "" {
		xhttp.ErrCode(c, "agent.invalid_path", nil, "invalid path", "")
		return
	}
	// Confirm the file exists before signing, or the user gets a ticket and only finds a 404 after clicking.
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

// DownloadByTicket is the navigation-style download: no Authorization header, authenticated by the signature in the URL.
// The route must be registered before the auth middleware (see admin.SetupRoutes).
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

	// The ticket carries the schema: a forged AuthInfo lets the repository query by tenant as usual (no session here).
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
	if jerr != nil || clean != t.Path { // clean != the ticket path means the path was normalised, reject it
		c.String(http.StatusBadRequest, "invalid path")
		return
	}
	ServeDownload(c, st, abs, clean)
}
