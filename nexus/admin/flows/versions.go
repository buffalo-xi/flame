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

// ListFlowVersions 工作流版本历史（新到旧，不含 diagram）。
//
// GET /admin/flows/:id/versions
func ListFlowVersions(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	list, err := repo.ListFlowVersions(ai, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"versions": list}, "")
}

// ReleaseFlowVersion 把当前工作副本冻结成一个新版本并设为当前。
//
// POST /admin/flows/:id/versions  body: {"note": "..."}
//
// 发布不是「上线开关」：正式入口用的就是这个版本，所以发布后线上行为立刻切到这一版，
// 旧版本仍留在历史里可回滚。发布前的草稿改动只影响编辑页调试。
func ReleaseFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	var body struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	v, err := repo.ReleaseFlowVersion(ai, c.Param("id"), strings.TrimSpace(body.Note))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "flow instance not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", v, "")
}

// RollbackFlowVersion 把「正在使用的版本」切到该历史版本（线上立刻生效）。
//
// POST /admin/flows/:id/versions/:version/rollback
//
// 纯指针操作：不新增版本、不动画布。想基于旧版本继续改，用下面的 ApplyFlowVersion。
func RollbackFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	v, err := repo.RollbackFlowVersion(ai, c.Param("id"), target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "version not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", v, "")
}

// ApplyFlowVersion 把该版本的内容载入画布（工作副本），不发布。
//
// POST /admin/flows/:id/versions/:version/apply
//
// 与回滚分开：这是「以旧版本为起点继续改」，线上当前版本不受影响；
// 载入后工作副本与当前版本不一致，界面会显示「有改动」，发布后才生效。
func ApplyFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	if err := repo.ApplyFlowVersionToDraft(ai, c.Param("id"), target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "version not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteFlowVersion 删除一条历史版本（当前版本不可删）。
//
// DELETE /admin/flows/:id/versions/:version
//
// 只清历史：线上跑的还是 is_current 那一版，画布草稿也不动。
// 要删当前版本，先用 POST .../rollback 把线上切到别的版本 —— 删除不该兼职停服。
func DeleteFlowVersion(c *gin.Context) {
	raw, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	ai := raw.(*auth.AuthInfo)

	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	if err := repo.DeleteFlowVersion(ai, c.Param("id"), target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "version not found", nil, "")
			return
		}
		if errors.Is(err, repository.ErrCannotDeleteCurrentVersion) {
			// 文案原样透出：这是给人看的指引（先去切线上），不是 400 参数错误
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}
