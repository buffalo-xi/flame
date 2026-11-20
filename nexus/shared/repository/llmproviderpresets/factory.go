package repository

// NewRepository creates the LLM provider preset storage instance.
func NewRepository() LLMProviderPresetRepository {
	return NewPostgresRepository()
}
