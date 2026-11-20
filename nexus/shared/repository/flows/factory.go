package repository

// NewRepository creates the workflow instance storage instance.
func NewRepository() Repository {
	return NewPostgresRepository()
}
