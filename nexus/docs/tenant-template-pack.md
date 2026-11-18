# 租户创建资源方案

新建租户的资产来源：一个**提交进仓库的模板包**。

一句话：t0 只用来生成这个包一次，**运行时完全不读 t0、也不读 `data/tenants/t0`**。

## 为什么是包，而不是从 t0 复制

1. t0 是开发机上的一个真实租户，生产环境并不存在 —— 按它复制等于把开发数据当成依赖。
2. 整库复制会把 t0 的业务数据（LLM 服务商、连接、工作流、会话、用户）一起带过去，新租户不干净。
3. 旧的 `tenant_template.sql` 是某次 t0 dump 的静态快照，已经落后（缺 `agents.is_supervisor`
   等只存在于增量迁移里的列），继续用它建表会开出缺列的租户。

## 包结构

```
nexus/platform/provision/template/
├── schema.sql      # 全部表结构（28 张表 / 约束 / 索引 / 注释，对象名不带 schema 前缀）
├── seed.sql        # 预置资产的数据行（固定 uuid）
└── files/
    ├── agents/main/AGENT.md
    ├── skills/skill-authoring/SKILL.md
    ├── skills/agent-authoring/SKILL.md
    ├── skills/custom-tool-authoring/SKILL.md
    └── skills/flow-authoring/SKILL.md
```

Go 侧整体嵌入：

```go
//go:embed template
var templateAssets embed.FS
```

## 开通流程（`provision.Provision`）

单个事务，顺序即设计：

1. `CREATE SCHEMA "<new>"`
2. `SELECT set_config('search_path', '<new>', true)` —— 包内 SQL 全部不带 schema 前缀
3. 执行 `template/schema.sql`
4. 执行 `template/seed.sql`（固定 uuid，**不幂等**：开通只做一次）
5. `INSERT users`（首个 `manage` 用户 `primary`，密码由调用方给）
6. 提交前物化 `template/files/` → storage 的 `tenants/<new>/<rel>`
7. `INSERT platform.tenants`
8. 写 `platform.tenant_capabilities`：`capability.All()` 的每一项 `is_allowed = true`（**缺省全开**）
9. `COMMIT`
10. 提交后失效缓存：`sharedtenants.Invalidate` / `quota.Invalidate`

### 关于缺省能力授权

新租户开箱就能用 exec / shell / code / browser / egress，靠的是**把行写出来**，而不是改缺省值：

`capabilitypolicy` 的语义是「没有行 = 未开通」（缺省拒绝）。如果把缺省改成放行，平台侧这层闸
对所有存量租户会整体失效 —— 租户又能给自己放行，等于没有闸。所以只能在开通时物化 5 行。

平台侧仍有两层可控：

- 逐项关闭：管理台「能力授权」把某租户的某项改成 `false`（或删行 = 回到未开通）
- 全局急停：`platform.capability_gates` 一旦拦下，压过租户的显式开启

同一能力的两层关系是 `生效 = 租户行 AND NOT 全局闸`。

任何一步失败都回滚事务；已写入的文件用 `DeletePrefix("tenants/<new>")` 回收 ——
保证「要么是一个完整租户，要么什么都没留下」。文件放在提交前写就是为了这一点。

## 预置资产口径

`seed.sql` 必须与下面两份 SQL 同口径，改一边要同步另一边，否则新租户开出来是半残的：

| 来源 | 内容 |
| --- | --- |
| `nexus/sql/main_core_tool_grants.sql` | main 的 6 个 required 组：`builtin:file` / `exec` / `shell` / `agent` / `skill` / `agent-admin` |
| `nexus/sql/supervisor_on_demand_groups.sql` | 超级智能体的 5 个 allow 组：`builtin:skill-own` / `skill-admin` / `tool-grant` / `custom-tool` / `flow-admin` |
| 同上 | 技能工具依赖：`skill-authoring`→`skill-own`+`skill-admin`；`agent-authoring`→`tool-grant`+`skill-admin`；`custom-tool-authoring`→`custom-tool`；`flow-authoring`→`flow-admin` |
| 同上 | main 绑定这四份技能（起步套件，system prompt 会带上它们的说明） |

固定 uuid（seed 里的授权行 / 绑定行靠它引用）：

| 实体 | uuid |
| --- | --- |
| agent `main` | `a0000000-0000-4000-8000-000000000001` |
| skill `skill-authoring` | `a0000000-0000-4000-8000-000000000011` |
| skill `agent-authoring` | `a0000000-0000-4000-8000-000000000012` |
| skill `custom-tool-authoring` | `a0000000-0000-4000-8000-000000000013` |
| skill `flow-authoring` | `a0000000-0000-4000-8000-000000000014` |

受保护技能名单的真源是 `shared/models/protected.go` 的 `protectedSkills`
（`models.ProtectedSkillNames()` 提供只读副本）。**加一个保护技能时，seed.sql 与 files/ 都要补**。

## 重新生成（源库结构变了才需要）

```powershell
cd d:/proj/flame
$env:PGPASSWORD='pg'
pg_dump -h localhost -p 5432 -U postgres -d flame --schema-only -n t0 --no-owner --no-privileges -f t0_ddl.sql

# 转成无 schema 前缀的版本：去 psql 元命令、SET、set_config、CREATE SCHEMA、注释行，再去 t0. 前缀
$lines = [System.IO.File]::ReadAllLines('d:/proj/flame/t0_ddl.sql', [System.Text.Encoding]::UTF8)
$out = New-Object System.Collections.Generic.List[string]
foreach ($line in $lines) {
  if ($line -match '^\\') { continue }
  if ($line -match '^SET ') { continue }
  if ($line -match 'set_config') { continue }
  if ($line -match '^CREATE SCHEMA ') { continue }
  if ($line -match '^\s*$') { continue }
  if ($line -match '^--') { continue }
  $out.Add(($line -replace '\bt0\.', ''))
}
[System.IO.File]::WriteAllText(
  'd:/proj/flame/nexus/platform/provision/template/schema.sql',
  ($out -join "`n"),
  (New-Object System.Text.UTF8Encoding($false)))
```

> **必须全程 UTF-8 读写**。PowerShell 的 `Get-Content` 默认按系统 ANSI 解码，
> 会把 `COMMENT ON … IS '中文'` 读成乱码再写回去（`-f` 加 `ReadAllLines(..., UTF8)` 可避开）。

预置文件（`files/`）来自开发机上 t0 的定义域目录，只在改模板时同步一次：

```powershell
cd d:/proj/flame/nexus
$dst = "platform/provision/template/files"
New-Item -ItemType Directory -Force "$dst/agents","$dst/skills" | Out-Null
Copy-Item -Recurse -Force "data/tenants/t0/agents/main" "$dst/agents/"
foreach ($s in 'skill-authoring','agent-authoring','custom-tool-authoring') {
  Copy-Item -Recurse -Force "data/tenants/t0/skills/$s" "$dst/skills/"
}
```

## 自检

不开服务，直接在事务里试跑一遍（跑完回滚，不留痕迹）：

```sql
BEGIN;
CREATE SCHEMA t_pack_verify;
SELECT set_config('search_path', 't_pack_verify', true);
\i 'd:/proj/flame/nexus/platform/provision/template/schema.sql'
\i 'd:/proj/flame/nexus/platform/provision/template/seed.sql'
SELECT name, is_supervisor, status FROM agents;              -- main / t / 1
SELECT count(*) FROM agent_tool_grants;                      -- 10
SELECT count(*) FROM skill_tool_grants;                      -- 5
SELECT count(*) FROM agent_skill_bindings;                   -- 3
ROLLBACK;
```

用 `psql -v ON_ERROR_STOP=1 -f verify.sql` 执行，任何一条语句失败都会中断。
