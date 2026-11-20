package sessions

// NewRepository creates the session storage instance.
func NewRepository() Repository {
	return &PostgresRepository{}
}
