package handler

import (
	"errors"
	"strings"

	presetrepo "flame/nexus/shared/repository/llmproviderpresets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// LLM 服务商预设（platform.llm_provider_presets）：**系统级全局共享**，由平台管理员维护。
//
// 租户侧只读（/admin/llmproviderpresets/）；点预设是把 name / protocol / url **复制**进自己的
// 服务商记录（无外键引用），所以这里删除预设不会破坏任何租户已有配置，也就不需要引用检查。
// 这条链路没有缓存，写完无需失效。
//
// 长度上限与建表 DDL 对齐（name 100 / icon 255 / protocol 50 / url 1024），在接口层先拦一道，
// 避免靠 PG 报错兜底。protocol 无 CHECK 约束（值域由前端下拉给出、也允许自定义）。
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

// toInput 校验并归一化入参（新增与编辑共用）。
// 长度按**字符数**校验，与 PG 的 varchar(n) 口径一致。
func (r *presetRequest) toInput() (*presetrepo.PresetInput, error) {
	name := strings.TrimSpace(r.Name)
	icon := strings.TrimSpace(r.Icon)
	protocol := strings.TrimSpace(r.Protocol)
	url := strings.TrimSpace(r.URL)

	switch {
	case name == "":
		return nil, errors.New("名称不能为空")
	case len([]rune(name)) > 100:
		return nil, errors.New("名称最多 100 个字符")
	case protocol == "":
		return nil, errors.New("协议不能为空")
	case len([]rune(protocol)) > 50:
		return nil, errors.New("协议最多 50 个字符")
	case url == "":
		return nil, errors.New("URL 不能为空")
	case len([]rune(url)) > 1024:
		return nil, errors.New("URL 最多 1024 个字符")
	case len([]rune(icon)) > 255:
		return nil, errors.New("图标路径最多 255 个字符")
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

// presetID 取路径里的 id 并校验是合法 uuid（非法直接 400，不让 PG 报类型错）。
func presetID(c *gin.Context) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(id); err != nil {
		xhttp.ErrJSON(c, "非法的预设 id", nil, "")
		return "", false
	}
	return id, true
}

// ListLLMProviderPresets GET /platform/system/llm-provider-presets
func ListLLMProviderPresets(c *gin.Context) {
	items, err := presetRepo.ListAll()
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list presets: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}

// CreateLLMProviderPreset POST /platform/system/llm-provider-presets
// id 由仓储层生成 uuid（表上无 DB 默认值），随响应回带。
func CreateLLMProviderPreset(c *gin.Context) {
	var req presetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	in, err := req.toInput()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	id, err := presetRepo.Create(in)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to create preset: "+err.Error(), nil, "")
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
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	in, err := req.toInput()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	n, err := presetRepo.Update(id, in)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to update preset: "+err.Error(), nil, "")
		return
	}
	if n == 0 {
		xhttp.ErrJSON(c, "预设不存在", nil, "")
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
		xhttp.ErrJSON(c, "Failed to delete preset: "+err.Error(), nil, "")
		return
	}
	if n == 0 {
		xhttp.ErrJSON(c, "预设不存在", nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id, "deleted": n}, "")
}
