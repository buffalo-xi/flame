package repository

// NewRepository creates the LLM model storage instance.
func NewRepository() LLMModelRepository {
	return NewPostgresRepository()
}
