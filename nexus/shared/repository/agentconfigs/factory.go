package repository

// NewRepository creates the agent_configs storage instance.
func NewRepository() Repository {
	return NewPostgresRepository()
}
