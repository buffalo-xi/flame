package repository

// NewRepository 创建LLM服务商存储实例
func NewRepository() LLMProviderRepository {
	return NewPostgresRepository()
}
