package svcauthbindings

// NewRepository 返回默认的存储实现。
func NewRepository() Repository {
	return &PostgresRepository{}
}
