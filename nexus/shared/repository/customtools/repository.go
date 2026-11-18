// Package customtools 租户自定义自定义工具的存取（表 {schema}.custom_tools）。
//
// 与 plugins 仓储的分工：
//   - plugins：磁盘上发现的实现 + 每租户一份配置（代码不在库里）
//   - customtools：租户自己的工具定义（元数据在库里、代码在盘上 tools/<name>/）
//
// 所有方法都以 schema 定位表：这张表在**租户库**里（与 skills / plugins 同级），
// 不是平台库 —— 自定义工具是租户自己的业务逻辑，不是平台能力。
package customtools

import (
	"crypto/rand"
	"database/sql"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

const tableName = "custom_tools"

// normalizeRequiredJSON 把 NOT NULL 的 jsonb 列补成合法空值。
//
// nil 会被 xsql.JSONB 写成 SQL NULL，直接撞 NOT NULL 约束；报错是数据库层的
// 「null value in column ...」，看的人根本不知道是「TOOL.json 少写一个键」。
// 写库前统一兜一次：参数 schema / 配置声明 / 配置值三列都不允许 NULL。
func normalizeRequiredJSON(t *models.CustomTool) {
	if t.Parameters.Data == nil {
		t.Parameters = xsql.JSONB{Data: []byte(`{"type":"object","properties":{}}`)}
	}
	if t.ConfigFields.Data == nil {
		t.ConfigFields = xsql.JSONB{Data: []byte("[]")}
	}
	if t.Configs.Data == nil {
		t.Configs = xsql.JSONB{Data: []byte("{}")}
	}
}

// columns 列清单集中一处：顺序与 scan 一一对应，改表时只动这里。
const columns = `id, name, display_name, description, parameters, output, lang, entry,
	timeout_ms, max_output_bytes, egress, config_fields, configs, status, created_by, created_at, updated_at`

// Repository 自定义工具仓储。
type Repository interface {
	// List 列出本租户全部自定义工具（含非正常态），按 name 升序。
	List(schema string) ([]models.CustomTool, error)
	// ListEnabled 只列 status=1 的（目录/执行用）。
	ListEnabled(schema string) ([]models.CustomTool, error)
	// Get 按 name 取；不存在返回 sql.ErrNoRows。
	Get(schema, name string) (*models.CustomTool, error)
	// Create 插入一行；ID 为空时自动生成。
	Create(schema string, t *models.CustomTool) error
	// Update 按 name 覆盖可编辑列（不动 created_*）。
	Update(schema string, t *models.CustomTool) error
	// Delete 按 name 删除元数据（盘上的代码由调用方决定是否一并删）。
	Delete(schema, name string) error
}

// NewRepository 创建仓储。
func NewRepository() Repository { return &postgresRepository{} }

type postgresRepository struct{}

func table(schema string) string { return schema + "." + tableName }

func (r *postgresRepository) List(schema string) ([]models.CustomTool, error) {
	return r.query(schema, `SELECT `+columns+` FROM `+table(schema)+` ORDER BY name`)
}

func (r *postgresRepository) ListEnabled(schema string) ([]models.CustomTool, error) {
	return r.query(schema, `SELECT `+columns+` FROM `+table(schema)+` WHERE status = 1 ORDER BY name`)
}

func (r *postgresRepository) query(schema, q string, args ...any) ([]models.CustomTool, error) {
	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.CustomTool, 0, 8)
	for rows.Next() {
		var t models.CustomTool
		if err := rows.Scan(
			&t.ID, &t.Name, &t.DisplayName, &t.Description, &t.Parameters, &t.Output,
			&t.Lang, &t.Entry, &t.TimeoutMS, &t.MaxOutputBytes, &t.Egress,
			&t.ConfigFields, &t.Configs, &t.Status, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *postgresRepository) Get(schema, name string) (*models.CustomTool, error) {
	var t models.CustomTool
	err := db.DB.QueryRow(
		`SELECT `+columns+` FROM `+table(schema)+` WHERE name = $1`, name,
	).Scan(
		&t.ID, &t.Name, &t.DisplayName, &t.Description, &t.Parameters, &t.Output,
		&t.Lang, &t.Entry, &t.TimeoutMS, &t.MaxOutputBytes, &t.Egress,
		&t.ConfigFields, &t.Configs, &t.Status, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *postgresRepository) Create(schema string, t *models.CustomTool) error {
	normalizeRequiredJSON(t)
	if t.ID == "" {
		t.ID = newID()
	}
	if t.Entry == "" {
		t.Entry = "main.py"
	}
	if t.TimeoutMS <= 0 {
		t.TimeoutMS = 60000
	}
	if t.MaxOutputBytes <= 0 {
		t.MaxOutputBytes = 65536
	}
	_, err := db.DB.Exec(
		`INSERT INTO `+table(schema)+`
		 (id, name, display_name, description, parameters, output, lang, entry,
		  timeout_ms, max_output_bytes, egress, config_fields, configs, status, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		t.ID, t.Name, t.DisplayName, t.Description, t.Parameters, t.Output, t.Lang, t.Entry,
		t.TimeoutMS, t.MaxOutputBytes, t.Egress, t.ConfigFields, t.Configs, t.Status, t.CreatedBy,
	)
	return err
}

func (r *postgresRepository) Update(schema string, t *models.CustomTool) error {
	normalizeRequiredJSON(t)
	res, err := db.DB.Exec(
		`UPDATE `+table(schema)+` SET
		     display_name=$2, description=$3, parameters=$4, output=$5, lang=$6, entry=$7,
		     timeout_ms=$8, max_output_bytes=$9, egress=$10, config_fields=$11, configs=$12,
		     status=$13, updated_at=CURRENT_TIMESTAMP
		 WHERE name=$1`,
		t.Name, t.DisplayName, t.Description, t.Parameters, t.Output, t.Lang, t.Entry,
		t.TimeoutMS, t.MaxOutputBytes, t.Egress, t.ConfigFields, t.Configs, t.Status,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *postgresRepository) Delete(schema, name string) error {
	_, err := db.DB.Exec(`DELETE FROM `+table(schema)+` WHERE name = $1`, name)
	return err
}

// newID 生成 UUIDv4。
//
// 自建而不是引依赖：全项目只此一处需要随机 id，加一个 uuid 包不划算。
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源失败时退回时间无关的常量会让主键冲突；这里直接 panic 更诚实：
		// 加密随机源不可用的进程本来也不该继续提供写服务。
		panic(fmt.Sprintf("customtools: generate id: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
