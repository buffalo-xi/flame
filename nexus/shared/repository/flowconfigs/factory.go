package repository

// NewRepository creates the flow_configs storage instance.
func NewRepository() Repository {
	return NewPostgresRepository()
}
