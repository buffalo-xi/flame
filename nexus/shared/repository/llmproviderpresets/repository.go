package repository

import "flame/nexus/shared/models"

// LLMProviderPresetRepository LLM服务商预设仓库接口。
//
// 预设是**系统级全局共享**数据（platform.llm_provider_presets），由平台管理员维护；
// 租户侧只是读取并"复制填充"到自己的服务商记录里，不存在外键引用。
type LLMProviderPresetRepository interface {
	// ListAll 获取所有LLM服务商预设
	ListAll() ([]models.LLMProviderPreset, error)
	// Create 新增预设，返回生成的 id（程序生成 uuid，表上无 DB 默认值）
	Create(in *PresetInput) (string, error)
	// Update 按 id 覆盖可编辑字段，返回受影响行数（0 = 该预设不存在）
	Update(id string, in *PresetInput) (int64, error)
	// Delete 按 id 删除，返回受影响行数（0 = 该预设不存在）
	Delete(id string) (int64, error)
}

// PresetInput 可写入字段。
//
// 与 models.LLMProviderPreset 分开：那个用 xsql.String/xsql.Bool，是"读"用的模型；
// 写侧只需要普通 Go 值，icon / description 传空串表示写 NULL。
// extra_params 不在其中：模型与读取都没用到它，新增走表默认 {}，编辑不动该列。
type PresetInput struct {
	Name        string
	Icon        string
	Description string
	Protocol    string
	URL         string
	IsPopular   bool
	SortOrder   int
}
