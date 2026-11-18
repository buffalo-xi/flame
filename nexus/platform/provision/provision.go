// Package provision 开通租户：一个事务内建好「新租户能登录控制台」所需的一切。
//
// 编排（顺序即设计）：
//  1. 先做全部校验（schema 名 / 是否被占用 / 管理员账号与密码规则）—— 任何一步不通过都不碰库
//  2. 单个事务内完成数据库侧全部动作：
//     CREATE SCHEMA "<schema>"
//     SELECT set_config('search_path', $1, true)   —— 事务级、参数化（不拼字符串）
//     执行模板包 template/schema.sql              —— 全部表结构
//     执行模板包 template/seed.sql                —— main + 受保护技能 + 授权绑定
//     INSERT INTO users ...                     —— 该租户首个 manage 用户
//     物化模板包 template/files/                   —— 写 tenants/<schema>/ 下的定义域文件
//     INSERT INTO platform.tenants ...             —— 注册表行（平台 schema，显式限定）
//     PostgreSQL 支持事务性 DDL，任一步失败整体回滚，不会留下半成品 schema。
//  3. 提交后失效缓存（shared/tenants 白名单 + quota）——
//     否则新租户最多要等一个 TTL 周期才能登录。
//
// 文件放在提交之前写：失败就回滚事务并 DeletePrefix 回收已写目录，
// 保证「要么是一个完整租户，要么什么都没留下」。
//
// schema 可以留空：NextSchema 自动探索（t0、t1、t2… 取第一个未被占用的）；
// 落库时被抢占则返回 ErrSchemaTaken，由接口层回 409，让操作者确认新候选后重试。
package provision

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/account"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
	sharedtenants "flame/nexus/shared/tenants"
	"flame/tools/xlog"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// templateAssets 租户创建资源方案的模板包（整体嵌入，运行时不再依赖任何具体租户）：
//
//	template/schema.sql  全部表结构（由 t0 dump 转换而来，对象名不带 schema 前缀）
//	template/seed.sql    预置资产的行（main + 四个受保护技能 + 授权 / 绑定，固定 uuid）
//	template/files/      定义域文件（agents/main/AGENT.md、skills/<name>/SKILL.md）
//
// 放在本包目录下是因为 //go:embed 只能嵌入本包及子目录。
//
//go:embed template
var templateAssets embed.FS

// templateFilesRoot 包内文件树的根；物化时按相对路径写到 tenants/<schema>/ 下。
const templateFilesRoot = "template/files"

// Request 开通租户入参。
type Request struct {
	Schema        string // 新租户 schema 名：^[a-z][a-z0-9_]{1,31}$
	DisplayName   string // 展示名；空则用 schema 名
	Plan          string // 套餐标记，可空
	OwnerUserID   string // 负责人（平台「用户」id），可空 = 管理员代管
	AdminName     string // 该租户首个 manage 用户（登录名）
	AdminPassword string // 至少 account.MinPasswordLength 位
	CreatedBy     string // 操作者（平台管理员 id），仅记录
}

// Result 开通结果（供接口回显，尤其是"首个管理员账号"要告诉调用方）。
type Result struct {
	Schema      string
	DisplayName string
	AdminName   string
	AdminUserID string
}

var (
	// schemaPattern 与 platform.tenants 的 CHECK 约束一致（DB 侧还有兜底）
	schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	// adminPattern 与平台用户登录名规则一致（handler/users.go 的 namePattern）
	adminPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// reservedSchemas 系统保留 schema，禁止开成租户
	reservedSchemas = map[string]bool{
		"pg_catalog":         true,
		"information_schema": true,
		"public":             true,
		"platform":           true,
	}
)

// DefaultAdminName 新租户首个管理员的登录名（管理台固定用它，不再让操作者手填）。
//
// 取值来自 shared/tenants.PrimaryUserName：租户侧按同一个常量做"根账号不可修改 / 不可删除"的守卫，
// 两边必须同源（platform 不能 import nexus/admin，反之亦然，所以常量放在 shared）。
const DefaultAdminName = sharedtenants.PrimaryUserName

// ErrSchemaTaken schema 已被占用：注册表已有该行，或库里已存在同名 schema。
// 接口层据此回 409 + 一个新候选，让操作者确认后重试。
var ErrSchemaTaken = errors.New("schema 已被占用")

// schemaAutoMax 自动探索尝试上限（t0…t9999），避免异常数据下死循环。
const schemaAutoMax = 10000

// SchemaAvailable schema 是否可用于开租户：注册表没有它，且库里没有同名 schema。
//
// 两处都要查：软删的注册行可能已 DROP SCHEMA；反过来，建到一半留下的孤儿 schema
// （或人工建的）可能没有注册行 —— 只看一处都会在 CREATE SCHEMA 撞墙。
func SchemaAvailable(schema string) (bool, error) {
	exists, err := tenantsrepo.Exists(schema)
	if err != nil {
		return false, fmt.Errorf("检查租户注册表失败: %w", err)
	}
	if exists {
		return false, nil
	}
	var n int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = $1`, schema).Scan(&n); err != nil {
		return false, fmt.Errorf("检查数据库 schema 失败: %w", err)
	}
	return n == 0, nil
}

// NextSchema 探索第一个可用租户 schema：t0、t1、t2…（从 0 起逐个 +1 试），跳过系统保留名。
//
// 先一次取全量已占用名单，再在内存里逐个试，避免每个候选一次数据库往返。
func NextSchema() (string, error) {
	taken, err := takenSchemas()
	if err != nil {
		return "", err
	}
	for i := 0; i <= schemaAutoMax; i++ {
		cand := "t" + strconv.Itoa(i)
		if reservedSchemas[cand] || taken[cand] {
			continue
		}
		return cand, nil
	}
	return "", fmt.Errorf("连续尝试 %d 个 schema 均被占用，请先清理无用租户", schemaAutoMax+1)
}

// takenSchemas 全部已占用的 schema 名 = 库里的 schema ∪ 注册表（两套事实都要算）。
func takenSchemas() (map[string]bool, error) {
	rows, err := db.DB.Query(
		`SELECT schema_name FROM information_schema.schemata
		 UNION
		 SELECT schema_name FROM ` + pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema) + `.tenants`)
	if err != nil {
		return nil, fmt.Errorf("读取已占用 schema 失败: %w", err)
	}
	defer rows.Close()

	taken := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		taken[s] = true
	}
	return taken, rows.Err()
}

// isDuplicateSchema 是否是「库中已存在同名 schema」（PG 42P06 duplicate_schema）。
func isDuplicateSchema(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "42P06"
}

// Provision 开通一个租户（幂等性：已注册则直接报错，不会重复创建）。
func Provision(req Request) (*Result, error) {
	schema := strings.TrimSpace(req.Schema)
	displayName := strings.TrimSpace(req.DisplayName)
	adminName := strings.TrimSpace(req.AdminName)

	// ---- 1) 校验（不碰库） ----
	if !schemaPattern.MatchString(schema) {
		return nil, fmt.Errorf("schema 名不合法：只允许小写字母开头、字母/数字/下划线，长度 2-32")
	}
	if reservedSchemas[schema] {
		return nil, fmt.Errorf("schema 名 %q 是系统保留名", schema)
	}
	if !adminPattern.MatchString(adminName) {
		return nil, fmt.Errorf("管理员登录名不合法：只允许字母/数字/下划线/横线，长度 1-64")
	}
	switch {
	case len(req.AdminPassword) < account.MinPasswordLength:
		return nil, fmt.Errorf("管理员密码至少 %d 位", account.MinPasswordLength)
	case len(req.AdminPassword) > account.MaxPasswordBytes:
		return nil, account.ErrPasswordTooLong
	}

	// 占用有两种：注册表里已有 / 库里已有同名 schema（含建到一半的孤儿）
	available, err := SchemaAvailable(schema)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, fmt.Errorf("%w：%q 已在注册表中或库中已有同名 schema（如需重建请先处理旧租户）", ErrSchemaTaken, schema)
	}
	if displayName == "" {
		displayName = schema
	}

	adminHash, err := account.HashPassword(req.AdminPassword)
	if err != nil {
		return nil, fmt.Errorf("密码哈希失败: %w", err)
	}
	adminID := uuid.NewString()

	// ---- 2) 单个事务：建 schema + 模板建表 + 首管理员 + 注册表行 ----
	// DDL 一律走主连接池：原先可配 platform.ddl_dsn 指向独立 DDL 账号，该配置已删除。
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // 提交后再 Rollback 是无害的 no-op

	// schema 名已过正则，这里仍用 QuoteIdentifier 转义（纵深防御）
	if _, err := tx.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		// 上面检查到这一步之间被抢占（并发开通 / 有人手工建了同名 schema）
		if isDuplicateSchema(err) {
			return nil, fmt.Errorf("%w：%q 在库中已存在", ErrSchemaTaken, schema)
		}
		return nil, fmt.Errorf("创建 schema 失败: %w", err)
	}
	// 事务级 search_path：模板里的对象名全部不限定 schema，靠它落到新 schema 上
	if _, err := tx.Exec(`SELECT set_config('search_path', $1, true)`, schema); err != nil {
		return nil, fmt.Errorf("设置 search_path 失败: %w", err)
	}
	// 模板包：先结构，再预置资产。两份脚本都假设 search_path 已指向新 schema。
	schemaSQL, err := templateAssets.ReadFile("template/schema.sql")
	if err != nil {
		return nil, fmt.Errorf("读取模板表结构失败: %w", err)
	}
	seedSQL, err := templateAssets.ReadFile("template/seed.sql")
	if err != nil {
		return nil, fmt.Errorf("读取模板预置资产失败: %w", err)
	}
	if _, err := tx.Exec(string(schemaSQL)); err != nil {
		return nil, fmt.Errorf("执行模板表结构失败: %w", err)
	}
	// seed 用固定 uuid，重复执行会撞主键 —— 开通是一次性动作，不做幂等
	if _, err := tx.Exec(string(seedSQL)); err != nil {
		return nil, fmt.Errorf("写入预置资产失败: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO users (id, name, display_name, email, phone, password, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, '', '', $4, 'manage', 'active', NOW(), NOW())`,
		adminID, adminName, adminName, adminHash); err != nil {
		return nil, fmt.Errorf("创建租户管理员失败: %w", err)
	}

	// 定义域文件在提交前物化：包内 files/ → tenants/<schema>/
	// 失败就回滚事务 + 清目录，不留「库建好了、main 却没有 AGENT.md」的半残租户
	if err := materializeTemplateFiles(context.Background(), schema); err != nil {
		cleanupTenantDir(schema)
		return nil, err
	}

	// 此刻 search_path 指向新租户 schema，平台表必须显式限定
	// （列口径与 repository/tenants.Create 保持一致，改列时两处同改）
	platformSchema := pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema)
	if _, err := tx.Exec(
		`INSERT INTO `+platformSchema+`.tenants
		   (schema_name, display_name, status, plan, owner_user_id, remark, created_at, updated_at, created_by)
		 VALUES ($1, $2, 'active', $3, NULLIF($4, '')::uuid, '', NOW(), NOW(), NULLIF($5, '')::uuid)`,
		schema, displayName, strings.TrimSpace(req.Plan),
		strings.TrimSpace(req.OwnerUserID), strings.TrimSpace(req.CreatedBy)); err != nil {
		return nil, fmt.Errorf("写入租户注册表失败: %w", err)
	}

	// 缺省能力授权：新租户开箱可用全部已登记能力（exec / shell / code / browser / egress）。
	// 平台仍可逐项关闭，全局闸仍可急停 —— 这里写的是租户行，不绕开任何一层。
	if err := grantDefaultCapabilities(tx, schema); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交失败: %w", err)
	}

	// ---- 4) 缓存失效（提交后再做，避免回滚时白失效） ----
	sharedtenants.Invalidate(schema)
	quota.Invalidate(schema)

	xlog.Info("[platform-provision] 已开通租户 %s（首个管理员 %s）", schema, adminName)
	return &Result{Schema: schema, DisplayName: displayName, AdminName: adminName, AdminUserID: adminID}, nil
}

// ErrTenantNotFound 注册表里没有这个租户：删除接口据此区分「本来就没有」与「删到一半失败」。
var ErrTenantNotFound = errors.New("租户不存在")

// Remove 彻底删除一个租户：注册表行 + 平台侧按 tenant_id 归属的配置 + 整个租户 schema。
//
// 顺序即设计（与 Provision 严格反向）：
//  1. 先确认注册表里有这一行 —— 只删「平台认识」的租户，
//     避免手滑把别的 schema（人工建的对象、别人的库）当成租户 DROP 掉
//  2. 单个事务内：清平台侧租户行 → DROP SCHEMA ... CASCADE → 删注册表行
//     注册表行放最后：它删掉后 SchemaAvailable 才会认为这个名字可用，
//     中途失败整体回滚，不会留下「注册表没了但 schema 还在」的孤儿
//  3. 存储目录不在这里删（本包只碰数据库）：由接口层调 storage.DeletePrefix
//
// 不可逆：该租户的智能体、技能、工作流、用户、会话记录全部随 schema 一起消失。
func Remove(schema string) error {
	schema = strings.TrimSpace(schema)
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("schema 名不合法：只允许小写字母开头、字母/数字/下划线，长度 2-32")
	}
	if reservedSchemas[schema] {
		return fmt.Errorf("schema 名 %q 是系统保留名", schema)
	}

	exists, err := tenantsrepo.Exists(schema)
	if err != nil {
		return fmt.Errorf("检查租户注册表失败: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w：%q", ErrTenantNotFound, schema)
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 平台侧的租户行：插件授权、能力授权、配额、工作流运行参数、智能体运行参数
	for _, table := range []string{
		"tenant_plugins", "tenant_capabilities", "quota_configs", "flow_configs", "agent_configs",
	} {
		if err := deleteTenantRows(tx, table, schema); err != nil {
			return fmt.Errorf("清理 %s 失败: %w", table, err)
		}
	}

	// schema 名已过正则，这里仍用 QuoteIdentifier 转义（纵深防御）
	if _, err := tx.Exec(`DROP SCHEMA IF EXISTS ` + pq.QuoteIdentifier(schema) + ` CASCADE`); err != nil {
		return fmt.Errorf("删除 schema 失败: %w", err)
	}

	platformSchema := pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema)
	if _, err := tx.Exec(`DELETE FROM `+platformSchema+`.tenants WHERE schema_name = $1`, schema); err != nil {
		return fmt.Errorf("删除租户注册表行失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交失败: %w", err)
	}
	return nil
}

// deleteTenantRows 清掉某张平台表里归属该租户的行。
//
// 用 to_regclass 先探测：各部署的脚本执行进度可能不同（比如早期没有 agent_configs），
// 不能让一张可选表缺失把整次删除拖垮。
func deleteTenantRows(tx *sql.Tx, table string, tenantID string) error {
	ps := config.Cfg.Database.PlatformSchema
	var exists sql.NullString
	if err := tx.QueryRow(`SELECT to_regclass($1)::text`, ps+"."+table).Scan(&exists); err != nil {
		return err
	}
	if !exists.Valid {
		return nil
	}
	_, err := tx.Exec(
		`DELETE FROM `+pq.QuoteIdentifier(ps)+`.`+pq.QuoteIdentifier(table)+` WHERE tenant_id = $1`,
		tenantID)
	return err
}

// grantDefaultCapabilities 给新租户写入全部能力的允许行（缺省全开）。
//
// 为什么要物化而不是"缺省放行"：capabilitypolicy 的语义是**缺省拒绝**，没有行 = 未开通。
// 想让新租户开箱可用，只能把行写出来；一旦改成"没行就放行"，
// 平台侧这层闸对所有存量租户会整体失效（等于回到"租户自己说了算"）。
//
// 全局闸（platform.capability_gates）仍压过这里：被急停的能力照样不可用。
func grantDefaultCapabilities(tx *sql.Tx, tenantID string) error {
	keys := capability.All()
	if len(keys) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(keys))
	for _, k := range keys {
		normalized = append(normalized, capability.Normalize(k))
	}

	platformSchema := pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema)
	_, err := tx.Exec(
		`INSERT INTO `+platformSchema+`.tenant_capabilities (tenant_id, capability, is_allowed, updated_at)
		 SELECT $1, k, true, CURRENT_TIMESTAMP FROM unnest($2::text[]) AS k
		 ON CONFLICT (tenant_id, capability) DO UPDATE
		    SET is_allowed = true, updated_at = CURRENT_TIMESTAMP`,
		tenantID, pq.Array(normalized))
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return fmt.Errorf("写入能力授权失败（请先执行 nexus/platform/sql/tenant_capabilities.sql）: %w", err)
		}
		return fmt.Errorf("写入能力授权失败: %w", err)
	}
	return nil
}

// materializeTemplateFiles 把包里的定义域文件写到新租户目录：
//
//	template/files/agents/main/AGENT.md → tenants/<schema>/agents/main/AGENT.md
//	template/files/skills/<name>/**     → tenants/<schema>/skills/<name>/**
//
// 只写文件、不建目录：本地实现 WriteFile 会自动 MkdirAll，
// S3 写对象即产生前缀（见 shared/storage）。
func materializeTemplateFiles(ctx context.Context, schema string) error {
	st, err := templateStorage()
	if err != nil {
		return fmt.Errorf("初始化存储失败: %w", err)
	}

	return fs.WalkDir(templateAssets, templateFilesRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// io/fs 的路径恒用正斜杠，直接当相对路径拼进租户根
		rel := strings.TrimPrefix(p, templateFilesRoot+"/")
		data, err := templateAssets.ReadFile(p)
		if err != nil {
			return err
		}
		if err := st.WriteFile(ctx, path.Join(paths.TenantsSubdir, schema, rel), data); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", rel, err)
		}
		return nil
	})
}

// cleanupTenantDir 清掉某租户在存储里的全部文件（开通失败时回收半成品）。
func cleanupTenantDir(schema string) {
	st, err := templateStorage()
	if err != nil {
		return
	}
	if derr := st.DeletePrefix(context.Background(), path.Join(paths.TenantsSubdir, schema)); derr != nil {
		xlog.Error("[platform-provision] 回收租户 %s 的存储目录失败: %v", schema, derr)
	}
}

// templateStorage 懒加载存储实例（与 service/agent.InitStorage 同配置）。
var (
	provStoreOnce sync.Once
	provStore     storage.Storage
	provStoreErr  error
)

func templateStorage() (storage.Storage, error) {
	provStoreOnce.Do(func() {
		if config.Cfg == nil {
			provStoreErr = errors.New("config not loaded")
			return
		}
		provStore, provStoreErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return provStore, provStoreErr
}
