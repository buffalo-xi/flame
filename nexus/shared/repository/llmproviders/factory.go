package repository

// NewRepository creates the LLM provider storage instance.
func NewRepository() LLMProviderRepository {
	return NewPostgresRepository()
}
