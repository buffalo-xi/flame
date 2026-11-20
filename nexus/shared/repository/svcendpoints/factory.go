package repository

// NewRepository creates the service endpoint repository instance.
func NewRepository() SvcEndpointRepository {
	return NewPostgresRepository()
}
