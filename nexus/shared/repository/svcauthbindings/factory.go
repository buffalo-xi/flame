package svcauthbindings

// NewRepository returns the default storage implementation.
func NewRepository() Repository {
	return &PostgresRepository{}
}
