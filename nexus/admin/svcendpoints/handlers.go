package svcendpoints

import (
	"database/sql"
	"flame/tools/xsql"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Package-level repository instance.
var repo = repository.NewRepository()

// GetSvcEndpoint returns an endpoint by id.
func GetSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	svcEndpoint, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "endpoint.not_found", gin.H{"name": id}, "Service endpoint not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", svcEndpoint, "")
}

// CreateSvcEndpoint creates an endpoint.
func CreateSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		TargetID    string     `json:"target_id"`
		TargetType  string     `json:"target_type"`
		Protocol    string     `json:"protocol"`
		InputSchema xsql.JSONB `json:"input_schema"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	svcEndpoint := models.SvcEndpoint{
		Name:        req.Name,
		Description: req.Description,
		TargetID:    req.TargetID,
		TargetType:  req.TargetType,
		Protocol:    req.Protocol,
		InputSchema: req.InputSchema,
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &svcEndpoint)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	svcEndpoint.ID = id
	xhttp.OkJSON(c, "Success", svcEndpoint, "")
}

// UpdateSvcEndpoint updates an endpoint.
func UpdateSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	var req struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		InputSchema xsql.JSONB `json:"input_schema"`
		Exposed     *bool      `json:"exposed"` // pointer so "not sent" is distinguishable; drives the expose / retract switch
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Flips the exposure only (name / description / input_schema untouched).
	if req.Exposed != nil {
		if err := repo.UpdateExposed(authInfo.(*auth.AuthInfo), id, *req.Exposed); err != nil {
			if err == sql.ErrNoRows {
				xhttp.ErrCode(c, "endpoint.not_found", gin.H{"name": id}, "Service endpoint not found", "")
				return
			}
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		xhttp.OkJSON(c, "Success", nil, "")
		return
	}

	svcEndpoint := models.SvcEndpoint{
		Name:        req.Name,
		Description: req.Description,
		InputSchema: req.InputSchema,
	}

	err := repo.Update(authInfo.(*auth.AuthInfo), id, &svcEndpoint)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "endpoint.not_found", gin.H{"name": id}, "Service endpoint not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteSvcEndpointsByTarget deletes every protocol endpoint of a target.
func DeleteSvcEndpointsByTarget(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	err := repo.DeleteByTarget(authInfo.(*auth.AuthInfo), req.TargetType, req.TargetID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// RetractSvcEndpoints retracts every protocol endpoint of a target (records stay, exposed=false).
func RetractSvcEndpoints(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	if err := repo.RetractByTarget(authInfo.(*auth.AuthInfo), req.TargetType, req.TargetID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteSvcEndpoint deletes an endpoint.
func DeleteSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	err := repo.Delete(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Service endpoint deleted successfully", id, "")
}

// ListSvcEndpoints lists endpoints.
func ListSvcEndpoints(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// Decode the request parameters.
	var req repository.ListSvcEndpointsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	svcEndpoints, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Build the response.
	response := gin.H{
		"items": svcEndpoints,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}
