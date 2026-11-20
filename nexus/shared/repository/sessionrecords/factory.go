package sessionrecords

// NewRepository creates the session record storage instance.
func NewRepository() Repository {
	return &PostgresRepository{}
}
