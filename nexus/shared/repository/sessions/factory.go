package sessions

// NewRepository 创建会话存储实例
func NewRepository() Repository {
	return &PostgresRepository{}
}
