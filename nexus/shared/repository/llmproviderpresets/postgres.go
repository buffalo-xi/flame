package repository

import (
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL实现的LLM服务商预设存储
type PostgresRepository struct{}

// NewPostgresRepository 创建PostgreSQL存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// ListAll 获取所有LLM服务商预设，按 is_popular DESC, sort_order ASC 排序
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

// Create 新增预设，返回生成的 id。
//
// id 由**程序生成**（表上没有 DB 默认值），与仓库里其它 uuid 主键表一致；
// icon / description 传空串写 NULL；extra_params 不写，走表默认 {}。
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

// Update 按 id 覆盖可编辑字段，返回受影响行数（0 = 该预设不存在）。
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

// Delete 按 id 删除，返回受影响行数（0 = 该预设不存在）。
// 无外键引用：租户点预设只是把 name / protocol / url 复制进自己的服务商记录。
func (r *PostgresRepository) Delete(id string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+tableName()+` WHERE id=$1::uuid`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// tableName 返回带 schema 的表名
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".llm_provider_presets"
}
