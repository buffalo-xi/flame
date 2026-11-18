package globalvars

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/secureval"
)

// PostgresRepository PostgreSQL 实现的全局变量存储
type PostgresRepository struct{}

// 列清单放一处：新增列时改这里，List/Get 两处自动跟上。
// updated_by 可 NULL，用 xsql.UUID 接（NULL → 空串）。
const columns = `name, value, kind, description, updated_by, updated_at`

// List 列出一个租户的全部变量（按 name 升序，界面直接按此顺序展示）。
func (r *PostgresRepository) List(schema string) ([]GlobalVar, error) {
	rows, err := db.DB.Query(`SELECT ` + columns + ` FROM ` + schema + `.global_vars ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]GlobalVar, 0)
	for rows.Next() {
		var v GlobalVar
		if err := rows.Scan(&v.Name, &v.Value, &v.Kind, &v.Description, &v.UpdatedBy, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if err := openValue(&v); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// Get 取一条；不存在返回 (nil, nil)。
func (r *PostgresRepository) Get(schema, name string) (*GlobalVar, error) {
	if name == "" {
		return nil, nil
	}
	var v GlobalVar
	err := db.DB.QueryRow(`SELECT `+columns+` FROM `+schema+`.global_vars WHERE name = $1`, name).
		Scan(&v.Name, &v.Value, &v.Kind, &v.Description, &v.UpdatedBy, &v.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := openValue(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Upsert 按 name 写入或覆盖。
//
// created_by 刻意只在 INSERT 时落：覆盖值是「改配置」，不是「换创建人」。
// updated_at 每次覆盖都刷新 —— 运行期缓存靠它判断是否要重新拉（见 shared/globalvars）。
func (r *PostgresRepository) Upsert(schema string, v *GlobalVar) error {
	if v == nil || v.Name == "" {
		return fmt.Errorf("全局变量缺少 name")
	}
	value, err := sealValue(v)
	if err != nil {
		return err
	}
	_, err = db.DB.Exec(`
		INSERT INTO `+schema+`.global_vars (name, value, kind, description, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5, NOW(), NOW())
		ON CONFLICT (name) DO UPDATE SET
			value = EXCLUDED.value,
			kind = EXCLUDED.kind,
			description = EXCLUDED.description,
			updated_by = EXCLUDED.updated_by,
			updated_at = NOW()`,
		v.Name, value, v.Kind, v.Description, v.UpdatedBy)
	return err
}

// Delete 删除一条，返回是否命中。
func (r *PostgresRepository) Delete(schema, name string) (bool, error) {
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.global_vars WHERE name = $1`, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// sealValue 返回该写进 value 列的内容：secret 加密，其余原样。
//
// 不改入参 v.Value —— 调用方（handler）拿到解密后的明文，出参还要用它，
// 顺手改成密文会让「保存成功」的响应里回显一串 enc:v1:...。
func sealValue(v *GlobalVar) (string, error) {
	if v.Kind != KindSecret || v.Value == "" {
		return v.Value, nil
	}
	sealed, err := secureval.Encrypt(v.Value)
	if err != nil {
		return "", fmt.Errorf("加密变量 %s 的值失败: %w", v.Name, err)
	}
	return sealed, nil
}

// openValue 读出后把 value 列还原成明文（原地改）。
//
// 非 secret、空值、以及加密功能上线前写入的明文，都由 secureval.Decrypt 原样返回。
func openValue(v *GlobalVar) error {
	if v.Kind != KindSecret || v.Value == "" {
		return nil
	}
	plain, err := secureval.Decrypt(v.Value)
	if err != nil {
		return fmt.Errorf("解密变量 %s 失败（密钥是否被换过？）: %w", v.Name, err)
	}
	v.Value = plain
	return nil
}
