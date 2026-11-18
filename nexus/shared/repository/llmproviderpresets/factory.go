package repository

// NewRepository 创建LLM服务商预设存储实例
func NewRepository() LLMProviderPresetRepository {
	return NewPostgresRepository()
}
