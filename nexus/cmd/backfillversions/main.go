// backfillversions 给存量实体回填 v1（发布快照）。
//
// 为什么需要它：版本化之后，正式入口只读 is_current，没有版本就拒绝（ErrNoReleasedVersion）。
// 存量实体在版本表里一行都没有 —— 数据库能靠 SQL 造行，但 v1 的内容要**复制目录**，
// 纯 SQL 做不到（见 nexus/sql/entity_versions.sql 的注释）。所以回填是一次性的程序，
// 不是迁移脚本。
//
// 顺序要求：**先跑完回填，再开运行时闸**（P1），否则正式调用会全线失败。
//
// 用法：
//
//	go run ./cmd/backfillversions                 # 全部租户
//	go run ./cmd/backfillversions -dry-run        # 只报告，不写库（版本目录试算后即删）
//	go run ./cmd/backfillversions -schema t0      # 只处理一个租户
//
// 幂等：只处理「还没有版本行」的实体，重复执行不会产生 v2。
// 单实体失败不中断：最后打印汇总并以非零退出码提示有人工待办。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/toolfile"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
	"flame/tools/xlog"
)

// backfillNote 回填版本的说明。写明是迁移，别让人误以为是谁主动发的版。
const backfillNote = "迁移：建版本表时的当前内容（回填 v1）"

type stats struct {
	released int
	skipped  int // 已有版本行
	conflict int // 版本目录已存在但没有行
	failed   int
}

func (s stats) String() string {
	return fmt.Sprintf("回填 %d，跳过 %d，目录冲突 %d，失败 %d",
		s.released, s.skipped, s.conflict, s.failed)
}

func main() {
	dryRun := flag.Bool("dry-run", false,
		"只报告，不写库；版本目录会试算清单后立即删除（用来提前暴露缺入口文件 / 超限）")
	onlySchema := flag.String("schema", "", "只处理指定租户 schema（默认全部）")
	flag.Parse()

	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		fail("加载配置失败: %v", err)
	}
	config.Cfg = cfg

	// db.InitDatabase 会打日志，日志器必须先就绪（否则 xlog 内部解引用 nil）。
	if err := xlog.Init("logs", "backfillversions"); err != nil {
		fail("初始化日志失败: %v", err)
	}
	defer xlog.Close()

	if err := db.InitDatabase(); err != nil {
		fail("连接数据库失败: %v", err)
	}
	defer func() { _ = db.DB.Close() }()

	st, err := storage.New(cfg.Agent.Storage, cfg.Agent.RootDir)
	if err != nil {
		fail("初始化存储失败（driver=%s）: %v", cfg.Agent.Storage.Driver, err)
	}

	schemas, err := versioning.TenantSchemas()
	if err != nil {
		fail("枚举租户失败: %v", err)
	}
	if *onlySchema != "" {
		found := false
		for _, s := range schemas {
			if s == *onlySchema {
				found = true
				break
			}
		}
		if !found {
			fail("租户 %q 不存在（或未建 agents 表）", *onlySchema)
		}
		schemas = []string{*onlySchema}
	}

	fmt.Printf("回填版本 v1：%d 个租户%s\n", len(schemas), mark(*dryRun, "（dry-run，不写库）"))
	bad := false
	for _, schema := range schemas {
		ctx := context.Background()
		fmt.Printf("\n== %s ==\n", schema)

		a := backfillAgents(ctx, st, schema, *dryRun)
		fmt.Printf("  agents:      %s\n", a)
		s := backfillSkills(ctx, st, schema, *dryRun)
		fmt.Printf("  skills:      %s\n", s)
		t := backfillTools(ctx, st, schema, *dryRun, cfg.Agent.RootDir)
		fmt.Printf("  customtools: %s\n", t)

		if a.failed+s.failed+t.failed > 0 {
			bad = true
		}
	}
	if bad {
		fmt.Println("\n有实体回填失败（见上方日志）。修好后可重复执行：本命令幂等。")
		os.Exit(1)
	}
	fmt.Println("\n完成。现在可以开启正式入口读 is_current 的闸（P1）。")
}

func mark(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// ===== 智能体 =====

func backfillAgents(ctx context.Context, st storage.Storage, schema string, dry bool) stats {
	var out stats
	rows, err := db.DB.Query(`SELECT a.id::text, a.name FROM ` + schema + `.agents a
		WHERE NOT EXISTS (SELECT 1 FROM ` + schema + `.agent_versions v WHERE v.agent_id = a.id)
		ORDER BY a.name`)
	if err != nil {
		fmt.Printf("  [agents] 查询失败: %v\n", err)
		out.failed++
		return out
	}
	targets := collect(rows)
	if len(targets) == 0 {
		return out
	}

	for _, t := range targets {
		dir := paths.VersionKey(schema, paths.VersionAgents, t.name, 1)
		if exists, _ := st.Exists(ctx, dir); exists {
			fmt.Printf("  [agents] %s: 版本目录已存在但库里没有行，跳过（需人工确认）: %s\n", t.name, dir)
			out.conflict++
			continue
		}

		// 废弃私有技能不进快照；私有技能元数据进 skills 列
		skip, err := versioning.DeprecatedPrivateSkillDirs(schema, t.id)
		if err != nil {
			fmt.Printf("  [agents] %s: 读取私有技能失败: %v\n", t.name, err)
			out.failed++
			continue
		}
		privates, err := versioning.PrivateSkillsSnapshot(schema, t.id)
		if err != nil {
			fmt.Printf("  [agents] %s: 生成技能快照失败: %v\n", t.name, err)
			out.failed++
			continue
		}

		draftKey, kerr := paths.EntityDraftKey(schema, paths.VersionAgents, t.name)
		if kerr != nil {
			fmt.Printf("  [agents] %s: %v\n", t.name, kerr)
			out.failed++
			continue
		}
		files, err := versions.Snapshot(ctx, st, draftKey, dir, paths.AgentEntryFile, skip)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [agents] %s: 冻结失败（%v）%s\n", t.name, err,
				hintEntryMissing(paths.AgentEntryFile))
			out.failed++
			continue
		}
		raw, err := versions.Encode(files)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		if dry {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [agents] %s: 将回填 v1（%d 个文件）\n", t.name, len(files))
			out.released++
			continue
		}

		err = insertAgentVersion(schema, t.id, raw, privates)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [agents] %s: 写入版本行失败: %v\n", t.name, err)
			out.failed++
			continue
		}
		fmt.Printf("  [agents] %s: v1（%d 个文件）\n", t.name, len(files))
		out.released++
	}
	return out
}

// insertAgentVersion 用 INSERT ... SELECT 从实体行取镜像列 —— 列清单只写一次，
// 实体表加列时这里不会悄悄漏掉（漏了会被 entity_versions.sql 的校验 3 抓到）。
func insertAgentVersion(schema, id string, files, skills []byte) error {
	_, err := db.DB.Exec(`INSERT INTO `+schema+`.agent_versions
		(agent_id, version, name, description, config, tools_unrestricted, skill_discovery,
		 files, skills, is_current, note, created_by)
		SELECT a.id, 1, a.name, a.description, a.config, a.tools_unrestricted, a.skill_discovery,
		       $2, $3, true, $4, a.created_by
		FROM `+schema+`.agents a WHERE a.id = $1`, id, files, skills, backfillNote)
	return err
}

// ===== 技能（只回填租户级；私有技能随 agent 版本） =====

func backfillSkills(ctx context.Context, st storage.Storage, schema string, dry bool) stats {
	var out stats
	rows, err := db.DB.Query(`SELECT s.id::text, s.name FROM ` + schema + `.skills s
		WHERE s.agent_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM ` + schema + `.skill_versions v WHERE v.skill_id = s.id)
		ORDER BY s.name`)
	if err != nil {
		fmt.Printf("  [skills] 查询失败: %v\n", err)
		out.failed++
		return out
	}
	targets := collect(rows)
	if len(targets) == 0 {
		return out
	}

	for _, t := range targets {
		dir := paths.VersionKey(schema, paths.VersionSkills, t.name, 1)
		if exists, _ := st.Exists(ctx, dir); exists {
			fmt.Printf("  [skills] %s: 版本目录已存在但库里没有行，跳过（需人工确认）: %s\n", t.name, dir)
			out.conflict++
			continue
		}

		draftKey, kerr := paths.EntityDraftKey(schema, paths.VersionSkills, t.name)
		if kerr != nil {
			fmt.Printf("  [skills] %s: %v\n", t.name, kerr)
			out.failed++
			continue
		}
		files, err := versions.Snapshot(ctx, st, draftKey, dir, paths.SkillEntryFile, nil)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: 冻结失败（%v）%s\n", t.name, err,
				hintEntryMissing(paths.SkillEntryFile))
			out.failed++
			continue
		}
		raw, err := versions.Encode(files)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		if dry {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: 将回填 v1（%d 个文件）\n", t.name, len(files))
			out.released++
			continue
		}

		// 工具依赖是声明，属定义：与 admin/skills 的发布同口径一并冻结
		deps, derr := versioning.SkillToolDepsSnapshot(schema, t.id)
		if derr != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: 读取工具依赖失败: %v\n", t.name, derr)
			out.failed++
			continue
		}
		_, err = db.DB.Exec(`INSERT INTO `+schema+`.skill_versions
			(skill_id, version, name, description, metadata, user_invocable, command_dispatch,
			 command_tool, restricted, files, tool_deps, is_current, note, created_by)
			SELECT s.id, 1, s.name, s.description, s.metadata, s.user_invocable, s.command_dispatch,
			       s.command_tool, s.restricted, $2, $4, true, $3, s.created_by
			FROM `+schema+`.skills s WHERE s.id = $1`, t.id, raw, backfillNote, deps)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [skills] %s: 写入版本行失败: %v\n", t.name, err)
			out.failed++
			continue
		}
		fmt.Printf("  [skills] %s: v1（%d 个文件）\n", t.name, len(files))
		out.released++
	}
	return out
}

// ===== 自定义工具 =====
//
// 工具是「目录即实体」：只回填**表里有行**的工具。盘上有目录、表里没行的手工工具
// 需要先走一次编辑/创建让它登记（版本行的 tool_id 有外键），这里只报告不猜。
func backfillTools(ctx context.Context, st storage.Storage, schema string, dry bool, rootDir string) stats {
	var out stats
	rows, err := db.DB.Query(`SELECT t.id::text, t.name FROM ` + schema + `.custom_tools t
		WHERE NOT EXISTS (SELECT 1 FROM ` + schema + `.tool_versions v WHERE v.tool_id = t.id)
		ORDER BY t.name`)
	if err != nil {
		fmt.Printf("  [customtools] 查询失败: %v\n", err)
		out.failed++
		return out
	}
	targets := collect(rows)
	if len(targets) == 0 {
		return out
	}

	store := customtoolstore.New(rootDir, schema)
	for _, t := range targets {
		dir := paths.VersionKey(schema, paths.VersionTools, t.name, 1)
		if exists, _ := st.Exists(ctx, dir); exists {
			fmt.Printf("  [customtools] %s: 版本目录已存在但库里没有行，跳过（需人工确认）: %s\n", t.name, dir)
			out.conflict++
			continue
		}

		// 合并视图（TOOL.json 优先）：发布出来的「定义」必须与运行时看到的一致
		tool, gerr := store.Get(schema, t.name)
		if errors.Is(gerr, sql.ErrNoRows) || tool == nil {
			fmt.Printf("  [customtools] %s: 表里有行但目录/契约缺失，跳过（需人工补齐）\n", t.name)
			out.failed++
			continue
		}
		if gerr != nil {
			fmt.Printf("  [customtools] %s: 读取工具失败: %v\n", t.name, gerr)
			out.failed++
			continue
		}

		draftKey, kerr := paths.EntityDraftKey(schema, paths.VersionTools, t.name)
		if kerr != nil {
			fmt.Printf("  [customtools] %s: %v\n", t.name, kerr)
			out.failed++
			continue
		}
		files, err := versions.Snapshot(ctx, st, draftKey, dir, tool.Entry, nil)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: 冻结失败（%v）%s\n", t.name, err,
				hintEntryMissing(tool.Entry))
			out.failed++
			continue
		}

		// 快照必须自带合法 TOOL.json（草稿里缺或坏时用合并视图补写）
		norm := models.ToolFileOf(tool)
		data, merr := models.MarshalToolFile(norm)
		if merr != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		hostDst := filepath.Join(rootDir, filepath.FromSlash(dir))
		if werr := toolfile.WriteAt(hostDst, norm); werr != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: 写入版本契约失败: %v\n", t.name, werr)
			out.failed++
			continue
		}
		files = versions.PutEntry(files, models.ToolFileName, data)

		raw, err := versions.Encode(files)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			out.failed++
			continue
		}
		if dry {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: 将回填 v1（%d 个文件）\n", t.name, len(files))
			out.released++
			continue
		}

		// configs 值不进版本：配置是运行时事实，不是定义
		_, err = db.DB.Exec(`INSERT INTO `+schema+`.tool_versions
			(tool_id, version, name, display_name, description, parameters, output, lang, entry,
			 timeout_ms, max_output_bytes, egress, config_fields, configs, files,
			 is_current, note, created_by)
			SELECT t.id, 1, t.name, t.display_name, t.description, t.parameters, t.output, t.lang,
			       t.entry, t.timeout_ms, t.max_output_bytes, t.egress, t.config_fields,
			       '{}'::jsonb, $2, true, $3, t.created_by
			FROM `+schema+`.custom_tools t WHERE t.id = $1`, t.id, raw, backfillNote)
		if err != nil {
			_ = st.DeletePrefix(ctx, dir)
			fmt.Printf("  [customtools] %s: 写入版本行失败: %v\n", t.name, err)
			out.failed++
			continue
		}
		fmt.Printf("  [customtools] %s: v1（%d 个文件）\n", t.name, len(files))
		out.released++
	}
	return out
}

// ===== 小工具 =====

type target struct{ id, name string }

func collect(rows *sql.Rows) []target {
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.name); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// hintEntryMissing 给最常见的失败（入口文件缺失 / 目录不在）补一句人话。
func hintEntryMissing(entry string) string {
	return "（多半是定义目录不存在或缺入口文件 " + strings.TrimSpace(entry) + "）"
}

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}
