package agents

import (
	"errors"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/sessions"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// HTTP-side consolidation of session ownership checks
//
//   Resolution itself (read the sid / validate the UUID / query the database /
//   compare the owner) lives entirely in sessions.Lookup. This file only turns
//   the three failure classes into responses, so that chat / stop / the file
//   endpoints / download tickets all say the same thing - otherwise the gap
//   between "no such session" and "not yours" could be used to enumerate sids.
// ============================================================================

// notOwnedSession is the single wording for "missing / not owned by this agent".
const notOwnedSession = "Session not found or not owned by this agent"

// respondSessionErr writes a response per error class. It returns false only so the
// call site reads well as `if !respondSessionErr(...) { return }`; it always writes
// a response.
func respondSessionErr(c *gin.Context, err error, notOwned string) bool {
	switch {
	case errors.Is(err, sessions.ErrSessionRequired), errors.Is(err, sessions.ErrSessionInvalid):
		// Argument problem: one code, the detail stays in msg for diagnosis and for curl
		xhttp.ErrCode(c, "common.invalid_params", nil, err.Error(), "")
	case errors.Is(err, sessions.ErrSessionForbidden):
		// The single wording for "missing / not yours", so sids cannot be enumerated
		xhttp.ErrCode(c, "session.not_found", nil, notOwned, "")
	default:
		// Storage-layer failure: keep the original text for diagnosis (same as before the refactor)
		xhttp.ErrCode(c, "common.internal", nil, "failed to get session: "+err.Error(), "")
	}
	return false
}

// lookupSessionForUser resolves a session for console entries, scoped by the session's
// owning user.
//
// A session is one user's private conversation under one agent, not a tenant-wide
// resource. Letting someone else in with a known sid causes four things at once, and
// none of them is a theoretical risk:
//   - session workspaces are organised per owning user (users/{uid}/sessions/{sid}/),
//     so a stranger continuing the conversation writes into someone else's directory and
//     can browse, download and delete the artifacts inside;
//   - session_records does not record who sent each turn (only owner + session), so after
//     a cross-user continuation the history cannot be attributed and two people end up on
//     one timeline;
//   - the shell / browser interactive processes are reused per session
//     (tools.KillInteractive), so a stranger takes over the other side's cwd, environment
//     variables and still-running processes;
//   - stop cancels every running execution of a session, so a stranger can interrupt
//     someone else's turn.
//
// All three entry points (HTTP chat / MCP / RPC) therefore scope by user
// (sessions.ResolveForUser): the session belongs to a user, while owner_type / owner_id
// only record the creator. When a parent and a child agent share one session the owner
// inevitably differs, and scoping by owner would reject the legitimate call_agent cascade.
func lookupSessionForUser(ai *auth.AuthInfo, schema, sessionID string) (*models.Session, error) {
	if ai == nil {
		return nil, sessions.ErrSessionForbidden
	}
	return sessions.ResolveForUser(schema, sessionID, ai.ID)
}

// sessionVisibleTo reports whether a session is visible to the signed-in user - the
// single rule for every console session entry.
//
// It mirrors ListSessions / ListByUser (the list only shows sessions with user_id = the
// current user), so "not in the list, but readable once you know the id" cannot happen.
//
// The predicate itself lives in models.Session.VisibleToUser: an empty user_id means a
// tenant-level session (no owning user, also absent from the list), which has no "someone
// else" and is allowed; a violation only occurs when user_id is non-empty and differs from
// the caller. Keeping it in the model lets RPC / MCP share the same implementation instead
// of drifting apart in three places.
func sessionVisibleTo(sess *models.Session, ai *auth.AuthInfo) bool {
	if ai == nil {
		return false
	}
	return sess.VisibleToUser(ai.ID)
}
