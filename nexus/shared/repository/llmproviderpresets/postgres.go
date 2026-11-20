package repository

import (
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of LLM provider preset storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// ListAll lists every LLM provider preset, ordered by is_popular DESC, sort_order ASC.
func (r *PostgresRepository) ListAll() ([]models.LLMProviderPreset, error) {
	query := `SELECT id, name, icon, description, protocol, url, is_popular, sort_order FROM ` +
		tableName() + ` ORDER BY is_popular DESC, sort_order ASC`
	rows, err := db.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.LLMProviderPreset
	for rows.Next() {
		var p models.LLMProviderPreset
		if err := rows.Scan(&p.ID, &p.Name, &p.Icon, &p.Description, &p.Protocol, &p.URL, &p.IsPopular, &p.SortOrder); err != nil {
			return nil, err
		}
		results = append(results, p)
	}

	return results, nil
}

// Create adds a preset and returns the generated id.
//
// The id is generated **in the program** (the table has no database default), consistent with the other uuid-keyed tables
// in the repository; an empty icon / description is written as NULL; extra_params is not written, so the table default {}
// applies.
func (r *PostgresRepository) Create(in *PresetInput) (string, error) {
	id := uuid.NewString()
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+`
		   (id, name, icon, description, protocol, url, is_popular, sort_order)
		 VALUES ($1::uuid, $2, NULLIF($3,''), NULLIF($4,''), $5, $6, $7, $8)`,
		id, in.Name, in.Icon, in.Description, in.Protocol, in.URL, in.IsPopular, in.SortOrder)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Update overwrites the editable fields by id, returning the number of rows affected (0 = no such preset).
func (r *PostgresRepository) Update(id string, in *PresetInput) (int64, error) {
	res, err := db.DB.Exec(
		`UPDATE `+tableName()+`
		    SET name=$2, icon=NULLIF($3,''), description=NULLIF($4,''), protocol=$5, url=$6,
		        is_popular=$7, sort_order=$8
		  WHERE id=$1::uuid`,
		id, in.Name, in.Icon, in.Description, in.Protocol, in.URL, in.IsPopular, in.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete deletes by id, returning the number of rows affected (0 = no such preset).
// There is no foreign key reference: picking a preset on the tenant side merely copies name / protocol / url into its own
// provider record.
func (r *PostgresRepository) Delete(id string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+tableName()+` WHERE id=$1::uuid`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// tableName returns the table name with its schema.
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".llm_provider_presets"
}
