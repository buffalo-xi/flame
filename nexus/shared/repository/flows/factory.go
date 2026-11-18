package repository

// NewRepository 创建工作流实例存储实例
func NewRepository() Repository {
	return NewPostgresRepository()
}
