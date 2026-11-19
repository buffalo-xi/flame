package handler

import (
	"errors"
	"strings"

	presetrepo "flame/nexus/shared/repository/llmproviderpresets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// LLM provider presets (platform.llm_provider_presets): **system-level and globally shared**,
// maintained by platform administrators.
//
// The tenant side is read-only (/admin/llmproviderpresets/); picking a preset **copies**
// name / protocol / url into the tenant's own provider record (no foreign key), so deleting a
// preset here breaks no existing tenant config and needs no reference check.
// This path has no cache, so nothing has to be invalidated after a write.
//
// The length limits match the table DDL (name 100 / icon 255 / protocol 50 / url 1024) and are
// enforced at the endpoint first, so PG errors are not the fallback. protocol has no CHECK
// constraint (the value set comes from the frontend dropdown, and custom values are allowed).
var presetRepo = presetrepo.NewRepository()

type presetRequest struct {
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	Protocol    string `json:"protocol"`
	URL         string `json:"url"`
	IsPopular   bool   `json:"is_popular"`
	SortOrder   int    `json:"sort_order"`
}

// toInput validates and normalises the request (shared by create and edit).
// Lengths are counted in **characters**, matching PG's varchar(n).
func (r *presetRequest) toInput() (*presetrepo.PresetInput, error) {
	name := strings.TrimSpace(r.Name)
	icon := strings.TrimSpace(r.Icon)
	protocol := strings.TrimSpace(r.Protocol)
	url := strings.TrimSpace(r.URL)

	switch {
	case name == "":
		return nil, errors.New("the name cannot be empty")
	case len([]rune(name)) > 100:
		return nil, errors.New("the name is limited to 100 characters")
	case protocol == "":
		return nil, errors.New("the protocol cannot be empty")
	case len([]rune(protocol)) > 50:
		return nil, errors.New("the protocol is limited to 50 characters")
	case url == "":
		return nil, errors.New("the URL cannot be empty")
	case len([]rune(url)) > 1024:
		return nil, errors.New("the URL is limited to 1024 characters")
	case len([]rune(icon)) > 255:
		return nil, errors.New("the icon path is limited to 255 characters")
	}

	return &presetrepo.PresetInput{
		Name:        name,
		Icon:        icon,
		Description: r.Description,
		Protocol:    protocol,
		URL:         url,
		IsPopular:   r.IsPopular,
		SortOrder:   r.SortOrder,
	}, nil
}

// presetID reads the id from the path and verifies it is a valid uuid (400 directly instead of
// letting PG raise a type error).
func presetID(c *gin.Context) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(id); err != nil {
		xhttp.ErrCode(c, "llm.preset_invalid_id", gin.H{"id": id}, "invalid preset id", "")
		return "", false
	}
	return id, true
}

// ListLLMProviderPresets GET /platform/system/llm-provider-presets
func ListLLMProviderPresets(c *gin.Context) {
	items, err := presetRepo.ListAll()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to list presets: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}

// CreateLLMProviderPreset POST /platform/system/llm-provider-presets
// The id is generated as a uuid by the repository (the table has no DB default) and returned in the
// response.
func CreateLLMProviderPreset(c *gin.Context) {
	var req presetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	in, err := req.toInput()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	id, err := presetRepo.Create(in)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to create preset: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}

// UpdateLLMProviderPreset PUT /platform/system/llm-provider-presets/:id
func UpdateLLMProviderPreset(c *gin.Context) {
	id, ok := presetID(c)
	if !ok {
		return
	}

	var req presetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	in, err := req.toInput()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	n, err := presetRepo.Update(id, in)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to update preset: "+err.Error(), "")
		return
	}
	if n == 0 {
		xhttp.ErrCode(c, "llm.preset_not_found", gin.H{"id": id}, "preset not found", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id, "updated": n}, "")
}

// DeleteLLMProviderPreset DELETE /platform/system/llm-provider-presets/:id
func DeleteLLMProviderPreset(c *gin.Context) {
	id, ok := presetID(c)
	if !ok {
		return
	}

	n, err := presetRepo.Delete(id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to delete preset: "+err.Error(), "")
		return
	}
	if n == 0 {
		xhttp.ErrCode(c, "llm.preset_not_found", gin.H{"id": id}, "preset not found", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id, "deleted": n}, "")
}
