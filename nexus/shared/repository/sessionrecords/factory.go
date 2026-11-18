package sessionrecords

// NewRepository 创建会话记录存储实例
func NewRepository() Repository {
	return &PostgresRepository{}
}
