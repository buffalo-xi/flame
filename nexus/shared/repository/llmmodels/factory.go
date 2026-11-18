package repository

// NewRepository 创建LLM模型存储实例
func NewRepository() LLMModelRepository {
	return NewPostgresRepository()
}
