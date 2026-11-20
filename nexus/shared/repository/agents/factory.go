package repository

// NewRepository creates the agent storage repository.
func NewRepository() Repository {
	return NewPostgresRepository()
}
