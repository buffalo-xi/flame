-- ============================================================================
-- 实体版本表：agent / skill / custom_tool / flow
-- ----------------------------------------------------------------------------
-- 一行 = 一次发布产生的不可变快照；实体表那一行是「工作副本」（草稿），不是版本。
-- `is_current` 用部分唯一索引保证「每个实体最多一版是当前版」，不在实体表放指针列。
--
-- 口径（与 template/schema.sql 逐字一致，两边必须同构）：
--   * 镜像列 = 实体表的定义列；不含 id / status / endpoints / agent_id / is_supervisor /
--     审计时间 —— 生命周期与对外状态不是版本内容。
--   * 正文（SKILL.md / AGENT.md / 工具脚本）不进表，files 只存清单（path/size/sha256），
--     内容目录在 tenants/<schema>/versions/{agents|skills|tools}/<name>/v<n>/。
--     版本域与草稿域平级（放实体目录里会让草稿目录不纯），且刻意不加点开头。
--   * 私有技能没有独立版本号：正文随 agent 版本的 files 清单，元数据进
--     agent_versions.skills —— 它在 agents/<a>/skills/** 里，是 agent 目录的一部分。
--   * 工作流例外：本体就是 flows.diagram，镜像过来即可，没有目录。
--
-- 本脚本只做「建表 + 回填工作流的 v1」——纯库，SQL 就能冻住完整定义。
-- 技能 / 智能体 / 工具的 v1 回填要复制内容目录，放在各自的发布接口里做。
--
-- 幂等：可重复执行（IF NOT EXISTS + 回填用 NOT EXISTS 防重）。
-- 若之前按 meta jsonb 的旧稿建过 *_versions，开发期先 DROP 再跑本脚本。
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/entity_versions.sql
-- ============================================================================
DO $$
DECLARE
  s text;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'agents'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    EXECUTE format($f$
      CREATE TABLE IF NOT EXISTS %I.flow_versions (
        flow_id uuid NOT NULL REFERENCES %I.flows(id) ON DELETE CASCADE,
        version integer NOT NULL,
        name varchar NOT NULL,
        description varchar,
        diagram jsonb,
        arguments jsonb,
        outputs jsonb,
        env_vars jsonb,
        flow_vars jsonb,
        timeout integer,
        max_recursion_depth integer,
        max_node_count integer,
        max_exec_count integer,
        max_run_records integer,
        max_session_records integer,
        is_current boolean NOT NULL DEFAULT false,
        note varchar(200) NOT NULL DEFAULT '',
        created_by uuid,
        created_at timestamptz NOT NULL DEFAULT now(),
        CONSTRAINT flow_versions_pkey PRIMARY KEY (flow_id, version)
      );
      CREATE UNIQUE INDEX IF NOT EXISTS flow_versions_current_idx
        ON %I.flow_versions (flow_id) WHERE is_current;
    $f$, s, s, s);

    EXECUTE format($f$
      CREATE TABLE IF NOT EXISTS %I.agent_versions (
        agent_id uuid NOT NULL REFERENCES %I.agents(id) ON DELETE CASCADE,
        version integer NOT NULL,
        name varchar(128) NOT NULL,
        description text,
        config jsonb,
        tools_unrestricted boolean,
        skill_discovery boolean,
        files jsonb NOT NULL DEFAULT '[]'::jsonb,
        skills jsonb NOT NULL DEFAULT '[]'::jsonb,
        is_current boolean NOT NULL DEFAULT false,
        note varchar(200) NOT NULL DEFAULT '',
        created_by uuid,
        created_at timestamptz NOT NULL DEFAULT now(),
        CONSTRAINT agent_versions_pkey PRIMARY KEY (agent_id, version)
      );
      CREATE UNIQUE INDEX IF NOT EXISTS agent_versions_current_idx
        ON %I.agent_versions (agent_id) WHERE is_current;
      -- 已有库升级：私有技能元数据快照（幂等）
      ALTER TABLE %I.agent_versions
        ADD COLUMN IF NOT EXISTS skills jsonb NOT NULL DEFAULT '[]'::jsonb;
    $f$, s, s, s, s);

    EXECUTE format($f$
      CREATE TABLE IF NOT EXISTS %I.skill_versions (
        skill_id uuid NOT NULL REFERENCES %I.skills(id) ON DELETE CASCADE,
        version integer NOT NULL,
        name varchar(64) NOT NULL,
        description text,
        metadata jsonb,
        user_invocable boolean,
        command_dispatch varchar,
        command_tool varchar,
        restricted boolean,
        files jsonb NOT NULL DEFAULT '[]'::jsonb,
        tool_deps jsonb NOT NULL DEFAULT '[]'::jsonb,
        is_current boolean NOT NULL DEFAULT false,
        note varchar(200) NOT NULL DEFAULT '',
        created_by uuid,
        created_at timestamptz NOT NULL DEFAULT now(),
        CONSTRAINT skill_versions_pkey PRIMARY KEY (skill_id, version)
      );
      -- 已有库升级：工具依赖属于技能定义，必须跟着版本冻结（幂等）
      ALTER TABLE %I.skill_versions
        ADD COLUMN IF NOT EXISTS tool_deps jsonb NOT NULL DEFAULT '[]'::jsonb;
      CREATE UNIQUE INDEX IF NOT EXISTS skill_versions_current_idx
        ON %I.skill_versions (skill_id) WHERE is_current;
    $f$, s, s, s, s);

    EXECUTE format($f$
      CREATE TABLE IF NOT EXISTS %I.tool_versions (
        tool_id uuid NOT NULL REFERENCES %I.custom_tools(id) ON DELETE CASCADE,
        version integer NOT NULL,
        name varchar(64) NOT NULL,
        display_name varchar(64),
        description text,
        parameters jsonb,
        output jsonb,
        lang varchar(16),
        entry varchar(128),
        timeout_ms integer,
        max_output_bytes integer,
        egress boolean,
        config_fields jsonb,
        configs jsonb,
        files jsonb NOT NULL DEFAULT '[]'::jsonb,
        is_current boolean NOT NULL DEFAULT false,
        note varchar(200) NOT NULL DEFAULT '',
        created_by varchar(64) NOT NULL DEFAULT '',
        created_at timestamptz NOT NULL DEFAULT now(),
        CONSTRAINT tool_versions_pkey PRIMARY KEY (tool_id, version)
      );
      CREATE UNIQUE INDEX IF NOT EXISTS tool_versions_current_idx
        ON %I.tool_versions (tool_id) WHERE is_current;
    $f$, s, s, s);

    -- 工作流回填 v1：把当前定义整行冻成第一版并设为当前
    EXECUTE format($f$
      INSERT INTO %I.flow_versions
        (flow_id, version, name, description, diagram, arguments, outputs, env_vars, flow_vars,
         timeout, max_recursion_depth, max_node_count, max_exec_count, max_run_records,
         max_session_records, is_current, note, created_by)
      SELECT f.id, 1, f.name, f.description, f.diagram, f.arguments, f.outputs, f.env_vars,
             f.flow_vars, f.timeout, f.max_recursion_depth, f.max_node_count, f.max_exec_count,
             f.max_run_records, f.max_session_records, true, '迁移：建版本表时的当前内容',
             f.created_by
        FROM %I.flows f
       WHERE NOT EXISTS (SELECT 1 FROM %I.flow_versions v WHERE v.flow_id = f.id)
    $f$, s, s, s);
  END LOOP;
END $$;

-- 校验 1：每个租户 schema 都应有这四张表
SELECT table_schema, table_name
  FROM information_schema.tables
 WHERE table_name IN ('flow_versions', 'agent_versions', 'skill_versions', 'tool_versions')
 ORDER BY table_schema, table_name;

-- 校验 2：每个 flow 应恰好一条 is_current，缺失则告警
DO $$
DECLARE
  s text;
  missing int;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'flow_versions'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
  LOOP
    EXECUTE format(
      'SELECT count(*) FROM %I.flows f
        WHERE NOT EXISTS (SELECT 1 FROM %I.flow_versions v
                           WHERE v.flow_id = f.id AND v.is_current)', s, s)
      INTO missing;
    IF missing > 0 THEN
      RAISE WARNING '%: % 个 flow 没有当前版本', s, missing;
    ELSE
      RAISE NOTICE '%: OK', s;
    END IF;
  END LOOP;
END $$;

-- 校验 3：实体表与版本表的镜像列必须一致（正常返回 0 行）。
-- 以后给实体表加定义列时忘了同步版本表，这条会立刻暴露。
-- 注意版本表名是单数（flow_versions / agent_versions / skill_versions / tool_versions），
-- 不能拿实体表名直接拼 "_versions"。
SELECT e.table_schema,
       m.version_table,
       e.column_name AS missing_in_versions
  FROM information_schema.columns e
  JOIN (VALUES ('flows', 'flow_versions'),
               ('agents', 'agent_versions'),
               ('skills', 'skill_versions'),
               ('custom_tools', 'tool_versions'))
       AS m(entity_table, version_table) ON m.entity_table = e.table_name
  LEFT JOIN information_schema.columns v
    ON v.table_schema = e.table_schema
   AND v.table_name = m.version_table
   AND v.column_name = e.column_name
 WHERE e.column_name NOT IN ('id', 'status', 'endpoints', 'agent_id', 'is_supervisor',
                             'created_by', 'updated_by',
                             'created_at', 'updated_at')
   AND v.column_name IS NULL
 ORDER BY e.table_schema, m.version_table, e.column_name;
