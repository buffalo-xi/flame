package repository

// NewRepository 创建 flow_configs 存储实例
func NewRepository() Repository {
	return NewPostgresRepository()
}
