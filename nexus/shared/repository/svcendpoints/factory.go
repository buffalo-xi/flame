package repository

// NewRepository 创建服务端点仓库实例
func NewRepository() SvcEndpointRepository {
	return NewPostgresRepository()
}
