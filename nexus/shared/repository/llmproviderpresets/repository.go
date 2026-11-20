package repository

import "flame/nexus/shared/models"

// LLMProviderPresetRepository is the LLM provider preset repository interface.
//
// A preset is **system-level global shared** data (platform.llm_provider_presets) maintained by the platform admin; the
// tenant side only reads it and "copies it over" into its own provider record, with no foreign key in between.
type LLMProviderPresetRepository interface {
	// ListAll lists every LLM provider preset.
	ListAll() ([]models.LLMProviderPreset, error)
	// Create adds a preset and returns the generated id (the uuid is generated in the program; the table has no database
	// default).
	Create(in *PresetInput) (string, error)
	// Update overwrites the editable fields by id, returning the number of rows affected (0 = no such preset).
	Update(id string, in *PresetInput) (int64, error)
	// Delete deletes by id, returning the number of rows affected (0 = no such preset).
	Delete(id string) (int64, error)
}

// PresetInput holds the writable fields.
//
// Kept apart from models.LLMProviderPreset: that one uses xsql.String/xsql.Bool and is the model for "reading"; the write
// side only needs plain Go values, and an empty icon / description means writing NULL.
// extra_params is not among them: neither the model nor the reads use it, so a new row takes the table default {} and an
// edit leaves the column alone.
type PresetInput struct {
	Name        string
	Icon        string
	Description string
	Protocol    string
	URL         string
	IsPopular   bool
	SortOrder   int
}
