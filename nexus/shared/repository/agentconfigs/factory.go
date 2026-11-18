package repository

// NewRepository 创建 agent_configs 存储实例
func NewRepository() Repository {
	return NewPostgresRepository()
}
