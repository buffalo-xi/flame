package repository

// NewRepository 创建智能体存储库
func NewRepository() Repository {
	return NewPostgresRepository()
}
