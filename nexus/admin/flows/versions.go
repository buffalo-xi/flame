package flows

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/repository/flows"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ListFlowVersions lists the version history (newest first, no diagram).
//
// GET /admin/flows/:id/versions
func ListFlowVersions(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	list, err := repo.ListFlowVersions(ai, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"versions": list}, "")
}

// ReleaseFlowVersion freezes the working copy into a new version and makes it current.
//
// POST /admin/flows/:id/versions  body: {"note": "..."}
//
// Release is not a "go live" switch: production uses this version, so releasing switches production
// immediately; older versions stay in the history for rollback, and draft edits before a release only affect debug runs.
func ReleaseFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	var body struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	v, err := repo.ReleaseFlowVersion(ai, c.Param("id"), strings.TrimSpace(body.Note))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "flow.not_found", nil, "flow instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", v, "")
}

// RollbackFlowVersion points "the version in use" at this historical version (production changes immediately).
//
// POST /admin/flows/:id/versions/:version/rollback
//
// A pure pointer flip: no new version, canvas untouched. To keep editing from an old version use ApplyFlowVersion below.
func RollbackFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	v, err := repo.RollbackFlowVersion(ai, c.Param("id"), target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", v, "")
}

// ApplyFlowVersion loads this version into the canvas (the working copy) without releasing.
//
// POST /admin/flows/:id/versions/:version/apply
//
// Kept apart from rollback: this is "keep editing from an old version", and the current production version is unaffected;
// afterwards the working copy differs from the current version, the UI shows "modified", and nothing takes effect until the next release.
func ApplyFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	if err := repo.ApplyFlowVersionToDraft(ai, c.Param("id"), target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteFlowVersion deletes one historical version (the current one cannot be deleted).
//
// DELETE /admin/flows/:id/versions/:version
//
// History only: production still runs the is_current version and the canvas draft is untouched.
// To delete the current one, POST .../rollback to another version first - deleting must not double as a service stop.
func DeleteFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	if err := repo.DeleteFlowVersion(ai, c.Param("id"), target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
			return
		}
		if errors.Is(err, repository.ErrCannotDeleteCurrentVersion) {
			// The text is passed through as is: it is guidance for a human (roll back first), not a 400 parameter error.
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}
