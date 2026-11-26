CREATE TABLE agent_relations (
    caller_id uuid NOT NULL,
    callee_id uuid NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
COMMENT ON TABLE agent_relations IS 'agent 调用授权：无行=不可调用(默认拒绝)；创建者自动获得对新 agent 的调用权';
CREATE TABLE agent_run_records (
    id uuid NOT NULL,
    agent_id uuid NOT NULL,
    session_id uuid,
    user_id uuid,
    run_type character varying(16),
    status character varying(16) DEFAULT 'ok'::character varying NOT NULL,
    err_msg text DEFAULT ''::text NOT NULL,
    duration_ms bigint DEFAULT 0 NOT NULL,
    started_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    finished_at timestamp with time zone,
    question text DEFAULT ''::text NOT NULL,
    question_size integer DEFAULT 0 NOT NULL,
    answer text DEFAULT ''::text NOT NULL,
    answer_truncated boolean DEFAULT false NOT NULL,
    answer_size integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
COMMENT ON TABLE agent_run_records IS 'agent run log (audit): one row per run, including stateless calls with no session; independent of session memory';
COMMENT ON COLUMN agent_run_records.id IS 'run identity; session_records.run_id points here (no separate run_id column: one run is one row)';
COMMENT ON COLUMN agent_run_records.session_id IS 'NULL = stateless run (no _meta.session_id); session memory never reads this table';
COMMENT ON COLUMN agent_run_records.status IS 'ok=completed / stopped=terminated on purpose / failed=errored (reason in err_msg)';
COMMENT ON COLUMN agent_run_records.answer IS 'preview only (bounded); the full answer lives in session_records';
CREATE TABLE agent_skill_bindings (
    agent_id uuid CONSTRAINT agent_skills_agent_id_not_null NOT NULL,
    skill_id uuid CONSTRAINT agent_skills_skill_id_not_null NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT agent_skills_created_at_not_null1 NOT NULL
);
COMMENT ON TABLE agent_skill_bindings IS 'agent 与 skill 的授权白名单：仅当 skills.restricted=true 时生效；无外键，级联由应用层事务保证';
CREATE TABLE agent_tool_grants (
    agent_id uuid NOT NULL,
    grant_kind character varying(8) NOT NULL,
    item_type character varying(8) NOT NULL,
    ref character varying(160) NOT NULL,
    updated_by uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_tool_grants_kind_check CHECK (((grant_kind)::text = ANY ((ARRAY['required'::character varying, 'allow'::character varying, 'deny'::character varying])::text[]))),
    CONSTRAINT agent_tool_grants_type_check CHECK (((item_type)::text = ANY ((ARRAY['group'::character varying, 'tool'::character varying])::text[])))
);
CREATE TABLE agents (
    id uuid CONSTRAINT agents_id_not_null1 NOT NULL,
    name character varying(128) CONSTRAINT agents_name_not_null1 NOT NULL,
    description text,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    status integer DEFAULT 1 NOT NULL,
    endpoints jsonb DEFAULT '[]'::jsonb,
    created_by uuid,
    updated_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    tools_unrestricted boolean DEFAULT false NOT NULL,
    skill_discovery boolean DEFAULT false NOT NULL,
    is_supervisor boolean DEFAULT false NOT NULL,
    -- Default reply language (BCP-47, e.g. zh-CN). NULL = not configured: the caller's _meta.language wins, then the
    -- input's own language, then the product default. It is part of the definition (released, versioned, packed).
    default_language character varying(35),
    CONSTRAINT agents_status_check CHECK ((status = ANY (ARRAY[1, 3])))
);
COMMENT ON COLUMN agents.status IS '1:正常,3:废弃（不设「锁定」：线上由版本管理，废弃自动收回端点）';
CREATE TABLE flow_breakpoints (
    flow_id character varying(64) CONSTRAINT pipe_breakpoints_pipe_id_not_null NOT NULL,
    node_id character varying(64) CONSTRAINT pipe_breakpoints_node_id_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP CONSTRAINT pipe_breakpoints_created_at_not_null NOT NULL
);
CREATE TABLE flow_node_records (
    id uuid CONSTRAINT pipe_node_records_id_not_null NOT NULL,
    run_id uuid CONSTRAINT pipe_node_records_run_id_not_null NOT NULL,
    node_id character varying(64) CONSTRAINT pipe_node_records_node_id_not_null NOT NULL,
    process_id character varying(255) DEFAULT ''::character varying CONSTRAINT pipe_node_records_process_id_not_null NOT NULL,
    node_type character varying(32) DEFAULT ''::character varying CONSTRAINT pipe_node_records_node_type_not_null NOT NULL,
    status character varying(32) CONSTRAINT pipe_node_records_status_not_null NOT NULL,
    output_vars jsonb,
    output_vars_truncated boolean DEFAULT false CONSTRAINT pipe_node_records_output_vars_truncated_not_null NOT NULL,
    output_vars_size integer DEFAULT 0 CONSTRAINT pipe_node_records_output_vars_size_not_null NOT NULL,
    local_vars jsonb,
    local_vars_truncated boolean DEFAULT false CONSTRAINT pipe_node_records_local_vars_truncated_not_null NOT NULL,
    local_vars_size integer DEFAULT 0 CONSTRAINT pipe_node_records_local_vars_size_not_null NOT NULL,
    run_logs jsonb DEFAULT '[]'::jsonb CONSTRAINT pipe_node_records_run_logs_not_null NOT NULL,
    run_logs_truncated boolean DEFAULT false CONSTRAINT pipe_node_records_run_logs_truncated_not_null NOT NULL,
    run_logs_lines integer DEFAULT 0 CONSTRAINT pipe_node_records_run_logs_lines_not_null NOT NULL,
    err_code character varying(32) DEFAULT ''::character varying CONSTRAINT pipe_node_records_err_code_not_null NOT NULL,
    err_msg text DEFAULT ''::text CONSTRAINT pipe_node_records_err_msg_not_null NOT NULL,
    err_stack text DEFAULT ''::text CONSTRAINT pipe_node_records_err_stack_not_null NOT NULL,
    retry_count integer DEFAULT 0 CONSTRAINT pipe_node_records_retry_count_not_null NOT NULL,
    error_action character varying(32) DEFAULT ''::character varying CONSTRAINT pipe_node_records_error_action_not_null NOT NULL,
    start_time timestamp with time zone,
    end_time timestamp with time zone,
    duration_ms bigint DEFAULT 0 CONSTRAINT pipe_node_records_duration_ms_not_null NOT NULL,
    input_vars jsonb,
    input_vars_truncated boolean DEFAULT false CONSTRAINT pipe_node_records_input_vars_truncated_not_null NOT NULL,
    input_vars_size integer DEFAULT 0 CONSTRAINT pipe_node_records_input_vars_size_not_null NOT NULL,
    extra jsonb
);
CREATE TABLE flow_run_records (
    id uuid CONSTRAINT pipe_run_records_run_id_not_null NOT NULL,
    flow_id uuid CONSTRAINT pipe_run_records_pipe_id_not_null NOT NULL,
    session_id uuid,
    user_id uuid,
    status character varying(32) CONSTRAINT pipe_run_records_status_not_null NOT NULL,
    result text DEFAULT ''::text CONSTRAINT pipe_run_records_result_not_null NOT NULL,
    err_msg text DEFAULT ''::text CONSTRAINT pipe_run_records_err_msg_not_null NOT NULL,
    node_count integer DEFAULT 0 CONSTRAINT pipe_run_records_node_count_not_null NOT NULL,
    duration_ms bigint DEFAULT 0 CONSTRAINT pipe_run_records_duration_ms_not_null NOT NULL,
    started_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP CONSTRAINT pipe_run_records_started_at_not_null NOT NULL,
    finished_at timestamp with time zone,
    run_type character varying(10),
    run_args jsonb,
    run_limits jsonb,
    run_flow_vars jsonb,
    run_flow_vars_truncated boolean DEFAULT false,
    run_flow_vars_size integer DEFAULT 0,
    progress jsonb DEFAULT '{}'::jsonb CONSTRAINT pipe_run_records_progress_not_null NOT NULL,
    progress_truncated boolean DEFAULT false CONSTRAINT pipe_run_records_progress_truncated_not_null NOT NULL,
    progress_size integer DEFAULT 0 CONSTRAINT pipe_run_records_progress_size_not_null NOT NULL,
    -- drafts 本次调试运行跟随过的草稿实体（[]=纯正式运行）。
    -- 记它的理由：调试跑的是工作副本，内容随时会变，事后必须能说清「当时跑的是哪几份」。
    drafts jsonb DEFAULT '[]'::jsonb CONSTRAINT pipe_run_records_drafts_not_null NOT NULL
);
CREATE TABLE flows (
    id uuid CONSTRAINT wf_instances_id_not_null NOT NULL,
    name character varying CONSTRAINT wf_instances_name_not_null NOT NULL,
    diagram jsonb,
    description character varying,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    arguments jsonb,
    outputs jsonb,
    env_vars jsonb,
    flow_vars jsonb,
    timeout integer DEFAULT '-1'::integer,
    max_exec_count integer DEFAULT '-1'::integer,
    store_runs boolean DEFAULT true NOT NULL,
    session_memory boolean DEFAULT true NOT NULL,
    created_by uuid,
    updated_by uuid,
    endpoints jsonb DEFAULT '[]'::jsonb,
    status smallint DEFAULT 1 CONSTRAINT flow_instances_status_not_null NOT NULL,
    CONSTRAINT flow_instances_status_check CHECK ((status = ANY (ARRAY[1, 3]))),
    CONSTRAINT flows_limits_range_check CHECK ((timeout >= '-1'::integer) AND (max_exec_count >= '-1'::integer))
);
COMMENT ON COLUMN flows.name IS '名字，唯一';
COMMENT ON COLUMN flows.diagram IS '流程json';
COMMENT ON COLUMN flows.description IS '描述';
COMMENT ON COLUMN flows.timeout IS '超时秒数（本流程的设计预算，不得超过租户上限）';
COMMENT ON COLUMN flows.max_exec_count IS '最大节点累计执行次数（本流程的设计预算，不得超过租户上限）';
COMMENT ON COLUMN flows.store_runs IS '是否保留历史执行记录（条数是租户配额）';
COMMENT ON COLUMN flows.session_memory IS '是否有session记忆（条数是租户配额；false = 无状态流程）';
COMMENT ON COLUMN flows.status IS '1:正常,3:废弃（工作流不设「锁定」：线上由版本管理，废弃自动收回端点）';

-- ===== 实体版本（agent / skill / custom_tool / flow 各一张） =====
--
-- 一行 = 一次发布产生的不可变快照；实体表那一行是「工作副本」（草稿），不是版本。
-- `is_current` 用部分唯一索引保证「每个实体最多一版是当前版」，不在实体表放指针列 ——
-- 两个真源一定会有对不上的那天。
--
-- 镜像列 = 实体表的定义列，**不含** id / status / endpoints / agent_id / is_supervisor /
-- 审计时间：生命周期与对外状态不是版本内容。加列时要同步两边，
-- 校验语句见 nexus/sql/entity_versions.sql。
--
-- 正文（SKILL.md / AGENT.md / 工具脚本）不进表：files 只存清单（相对路径 + size + sha256），
-- 内容目录在 tenants/<schema>/versions/{agents|skills|tools}/<name>/v<n>/。
-- 版本域与草稿域平级、放租户根下（不放进实体目录）：草稿目录必须只装草稿内容，
-- 否则 dirty 比对 / 打包 / 载入 / 文件树都要额外记排除表。刻意不加点开头 ——
-- 它是正式数据，必须被备份正常带走；可达性由 gate.go 的路径 switch 兜住。
-- 私有技能没有独立版本号：它在 agents/<a>/skills/** 里，随所属 agent 的版本一起冻结
-- （正文进 agent_versions.files，元数据进 agent_versions.skills）。
-- 工作流例外：本体就是 flows.diagram 这一列，镜像过来即可，没有目录。
CREATE TABLE flow_versions (
    flow_id uuid NOT NULL,
    version integer NOT NULL,
    name character varying NOT NULL,
    description character varying,
    diagram jsonb,
    arguments jsonb,
    outputs jsonb,
    env_vars jsonb,
    flow_vars jsonb,
    timeout integer,
    max_exec_count integer,
    store_runs boolean,
    session_memory boolean,
    is_current boolean DEFAULT false NOT NULL,
    note character varying(200) DEFAULT ''::character varying NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT flow_versions_pkey PRIMARY KEY (flow_id, version)
);
CREATE UNIQUE INDEX flow_versions_current_idx ON flow_versions (flow_id) WHERE is_current;
COMMENT ON TABLE flow_versions IS '工作流版本历史：每发布一次插一行，is_current 唯一指向当前版本';

CREATE TABLE agent_versions (
    agent_id uuid NOT NULL,
    version integer NOT NULL,
    name character varying(128) NOT NULL,
    description text,
    config jsonb,
    tools_unrestricted boolean,
    skill_discovery boolean,
    default_language character varying(35),
    files jsonb DEFAULT '[]'::jsonb NOT NULL,
    -- 私有技能的元数据快照（name / description / metadata / user_invocable /
    -- command_dispatch / command_tool / restricted / tool_deps）。
    -- 私有技能没有独立版本号，正文在 files 清单里、元数据在这里：
    -- 只冻正文不冻描述，就会出现「只改描述不用发布也改变了模型看到的技能清单口径」。
    skills jsonb DEFAULT '[]'::jsonb NOT NULL,
    is_current boolean DEFAULT false NOT NULL,
    note character varying(200) DEFAULT ''::character varying NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_versions_pkey PRIMARY KEY (agent_id, version)
);
CREATE UNIQUE INDEX agent_versions_current_idx ON agent_versions (agent_id) WHERE is_current;
COMMENT ON TABLE agent_versions IS '智能体版本历史：镜像 agents 的定义列，files 是 AGENT.md 与附属文件清单，skills 是私有技能元数据快照';

CREATE TABLE skill_versions (
    skill_id uuid NOT NULL,
    version integer NOT NULL,
    name character varying(64) NOT NULL,
    description text,
    metadata jsonb,
    user_invocable boolean,
    command_dispatch character varying,
    command_tool character varying,
    restricted boolean,
    files jsonb DEFAULT '[]'::jsonb NOT NULL,
    -- 声明的工具依赖快照（[{"type":"group","ref":"builtin:exec"}]）。
    -- 依赖是**声明**，属定义，必须跟着版本走；能否真调到仍由 allow / deny 决定。
    tool_deps jsonb DEFAULT '[]'::jsonb NOT NULL,
    is_current boolean DEFAULT false NOT NULL,
    note character varying(200) DEFAULT ''::character varying NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT skill_versions_pkey PRIMARY KEY (skill_id, version)
);
CREATE UNIQUE INDEX skill_versions_current_idx ON skill_versions (skill_id) WHERE is_current;
COMMENT ON TABLE skill_versions IS '技能版本历史：租户级与 agent 私有共用一张表，按 skill_id 区分';

CREATE TABLE tool_versions (
    tool_id uuid NOT NULL,
    version integer NOT NULL,
    name character varying(64) NOT NULL,
    display_name character varying(64),
    description text,
    parameters jsonb,
    output jsonb,
    lang character varying(16),
    entry character varying(128),
    timeout_ms integer,
    max_output_bytes integer,
    egress boolean,
    config_fields jsonb,
    configs jsonb,
    files jsonb DEFAULT '[]'::jsonb NOT NULL,
    is_current boolean DEFAULT false NOT NULL,
    note character varying(200) DEFAULT ''::character varying NOT NULL,
    created_by character varying(64) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tool_versions_pkey PRIMARY KEY (tool_id, version)
);
CREATE UNIQUE INDEX tool_versions_current_idx ON tool_versions (tool_id) WHERE is_current;
COMMENT ON TABLE tool_versions IS '自定义工具版本历史：config_fields/configs 一并快照，files 是脚本清单';

CREATE TABLE llm_models (
    id uuid CONSTRAINT llm_source_models_id_not_null NOT NULL,
    name character varying CONSTRAINT llm_source_models_name_not_null NOT NULL,
    extra_params jsonb DEFAULT '{}'::jsonb,
    provider_id uuid CONSTRAINT llm_source_models_source_id_not_null NOT NULL,
    is_default boolean,
    context_window integer,
    capabilities jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    gateway_id character varying,
    gateway_model character varying,
    base_params jsonb DEFAULT '{}'::jsonb
);
CREATE TABLE llm_providers (
    id uuid CONSTRAINT llm_sources_id_not_null NOT NULL,
    name character varying CONSTRAINT llm_sources_name_not_null NOT NULL,
    url character varying,
    protocol character varying,
    api_key character varying,
    icon character varying,
    note character varying,
    enabled boolean DEFAULT false,
    extra_config jsonb DEFAULT '{}'::jsonb
);
CREATE TABLE mcp_connection_tools (
    id uuid NOT NULL,
    connection_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    input_schema jsonb,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE mcp_connections (
    id uuid NOT NULL,
    name character varying(255) DEFAULT ''::character varying NOT NULL,
    description character varying(1000),
    url character varying(512),
    command character varying(512),
    args jsonb DEFAULT '[]'::jsonb,
    transport character varying(50) DEFAULT 'streamable_http'::character varying NOT NULL,
    auth jsonb DEFAULT '{}'::jsonb,
    is_active boolean DEFAULT false CONSTRAINT mcp_connections_enabled_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    work_dir character varying DEFAULT ''::character varying,
    env jsonb,
    display_name character varying(100) DEFAULT ''::character varying,
    CONSTRAINT mcp_connections_transport_check CHECK (((transport)::text = ANY (ARRAY[('streamable_http'::character varying)::text, ('stdio'::character varying)::text])))
);
CREATE TABLE mcp_server_configs (
    id uuid NOT NULL,
    auth_type character varying(20) NOT NULL,
    auth_config jsonb DEFAULT '{}'::jsonb,
    is_active boolean DEFAULT true,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT mcp_server_configs_check CHECK (((auth_type)::text = ANY (ARRAY[('apikey'::character varying)::text, ('oauth'::character varying)::text])))
);
CREATE TABLE plugins (
    id uuid NOT NULL,
    name character varying(64) NOT NULL,
    impl character varying(64) DEFAULT ''::character varying NOT NULL,
    fallbacks jsonb DEFAULT '[]'::jsonb,
    configs jsonb DEFAULT '{}'::jsonb,
    is_active boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_used_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    last_error_at timestamp with time zone,
    kind character varying(16) DEFAULT 'tools'::character varying NOT NULL
);
CREATE TABLE rpc_connection_tools (
    id uuid NOT NULL,
    connection_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    input_schema jsonb,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE rpc_connections (
    id uuid NOT NULL,
    name character varying(255) DEFAULT ''::character varying NOT NULL,
    description character varying(1000),
    url character varying(512),
    command character varying(512),
    args jsonb DEFAULT '[]'::jsonb,
    transport character varying(50) DEFAULT 'streamable_http'::character varying NOT NULL,
    auth jsonb DEFAULT '{}'::jsonb,
    is_active boolean DEFAULT false CONSTRAINT rpc_connections_enabled_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    work_dir character varying DEFAULT ''::character varying,
    env jsonb,
    display_name character varying(100) DEFAULT ''::character varying,
    CONSTRAINT rpc_connections_transport_check CHECK (((transport)::text = ANY (ARRAY[('streamable_http'::character varying)::text, ('stdio'::character varying)::text])))
);
CREATE TABLE rpc_server_configs (
    id uuid NOT NULL,
    auth_type character varying(20) NOT NULL,
    auth_config jsonb DEFAULT '{}'::jsonb,
    is_active boolean DEFAULT true,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT rpc_server_configs_check CHECK (((auth_type)::text = ANY (ARRAY[('apikey'::character varying)::text, ('oauth'::character varying)::text])))
);
CREATE TABLE custom_tools (
    id uuid NOT NULL,
    name character varying(64) NOT NULL,
    display_name character varying(64) DEFAULT ''::character varying NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    parameters jsonb DEFAULT '{}'::jsonb NOT NULL,
    output jsonb,
    lang character varying(16) NOT NULL,
    entry character varying(128) DEFAULT 'main.py'::character varying NOT NULL,
    timeout_ms integer DEFAULT 60000 NOT NULL,
    max_output_bytes integer DEFAULT 65536 NOT NULL,
    egress boolean DEFAULT false NOT NULL,
    config_fields jsonb DEFAULT '[]'::jsonb NOT NULL,
    configs jsonb DEFAULT '{}'::jsonb NOT NULL,
    status integer DEFAULT 1 NOT NULL,
    created_by character varying(64) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE session_records (
    id uuid CONSTRAINT pipe_session_memory_id_not_null NOT NULL,
    owner_id uuid CONSTRAINT pipe_session_memory_pipe_id_not_null NOT NULL,
    session_id uuid CONSTRAINT pipe_session_memory_session_id_not_null NOT NULL,
    run_id uuid CONSTRAINT pipe_session_memory_run_id_not_null NOT NULL,
    input jsonb,
    output text,
    run_type character varying(16),
    created_at timestamp with time zone DEFAULT now() CONSTRAINT pipe_session_memory_created_at_not_null NOT NULL,
    owner_type character varying,
    duration_ms bigint DEFAULT 0 NOT NULL,
    status character varying(16) DEFAULT 'ok'::character varying NOT NULL,
    error text
);
COMMENT ON COLUMN session_records.duration_ms IS '本轮执行耗时（毫秒）；0=未记录';
COMMENT ON COLUMN session_records.status IS '本轮结果：ok=正常完成 / stopped=被主动终止 / failed=出错（原因见 error）';
COMMENT ON COLUMN session_records.error IS '失败或终止的人话原因；status=ok 时为空';
-- 会话工作区用量计数：只在「没有 Redis」的部署里作为计数后端（见 shared/quota/usage.go）。
CREATE TABLE session_usage (
    scope_key text NOT NULL,
    used_bytes bigint DEFAULT 0 NOT NULL,
    verified_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT session_usage_pkey PRIMARY KEY (scope_key)
);
COMMENT ON TABLE session_usage IS '会话工作区用量计数（无 Redis 部署时的配额后端）';

-- 单用户配额覆盖：NULL / 无行 = 跟随租户（见 shared/quota/quota.go）
CREATE TABLE user_quotas (
    user_id uuid NOT NULL,
    max_sessions int NULL,
    max_session_storage_bytes bigint NULL,
    updated_by uuid NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT user_quotas_pkey PRIMARY KEY (user_id)
);
COMMENT ON TABLE user_quotas IS '单用户配额覆盖（NULL/无行 = 跟随租户）';

-- 全局变量：本租户内所有智能体 / 工作流 / 技能可引用，一处改值处处生效。
-- 资产（工作流 / 智能体 / 技能的资产包）里只留引用名，值不出租户、不进包 ——
-- 这是「导出导入不把值写死在资产里」的落点。
-- 命名一致：与 flows.env_vars（环境变量，流程级）、flows.flow_vars（流程变量）
-- 同属 <scope>_vars 一族；全局作用域的 scope 就是 global。
-- 引用方式见 docs：工作流在节点 config.inputs 里声明 {source_type: "global", source_key: "名字"}，
-- 模板写 {{ .input.名字 }}；智能体 / 技能文档写 ${global_vars.X}。
-- 已下线（别再写）：模板级 {{.global_vars.X}}、环境变量 {type: "global", ...} —— 前者流程载入即报错。
-- kind=secret 的值在落库前由服务端加密（enc:v1:...，AES-256-GCM，见 shared/secureval），
-- 读出时解密；接口出参一律掩码。加密是应用层的，直接读库看不到明文 —— 但也意味着
-- 换密钥会让这些值解不开，密钥来源见 config 的 security.value_key。
CREATE TABLE global_vars (
    name character varying(64) NOT NULL,
    value text DEFAULT ''::text NOT NULL,
    kind character varying(16) DEFAULT 'text'::character varying NOT NULL,
    description character varying(200) DEFAULT ''::character varying NOT NULL,
    created_by uuid,
    updated_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT global_vars_pkey PRIMARY KEY (name)
);
COMMENT ON TABLE global_vars IS '全局变量：本租户内全局，值不出租户、不进资产包';
COMMENT ON COLUMN global_vars.kind IS 'text / secret（接口掩码 + 落库加密、不应在提示词里引用）/ number / bool';
COMMENT ON COLUMN global_vars.description IS '给人看的用途说明；导出资产包时随需求清单一起带出';

CREATE TABLE sessions (
    id uuid NOT NULL,
    owner_type character varying(16) NOT NULL,
    owner_id uuid NOT NULL,
    title character varying(200) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    user_id uuid
);
CREATE TABLE skill_tool_grants (
    skill_id uuid NOT NULL,
    item_type character varying(8) NOT NULL,
    ref character varying(160) NOT NULL,
    CONSTRAINT skill_tool_grants_type_check CHECK (((item_type)::text = ANY ((ARRAY['group'::character varying, 'tool'::character varying])::text[])))
);
CREATE TABLE skills (
    id uuid CONSTRAINT agent_skills_id_not_null NOT NULL,
    name character varying(64) CONSTRAINT agent_skills_name_not_null NOT NULL,
    description text DEFAULT ''::text CONSTRAINT agent_skills_description_not_null NOT NULL,
    created_by uuid,
    updated_by uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    status smallint DEFAULT 1 CONSTRAINT agent_skills_status_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT agent_skills_created_at_not_null NOT NULL,
    updated_at timestamp with time zone DEFAULT now() CONSTRAINT agent_skills_updated_at_not_null NOT NULL,
    agent_id uuid,
    user_invocable boolean DEFAULT false,
    command_dispatch character varying,
    command_tool character varying,
    restricted boolean DEFAULT true NOT NULL
);
COMMENT ON COLUMN skills.metadata IS '扩展元数据：恒为非空 JSON 对象，空对象表示无扩展字段（NOT NULL）';
COMMENT ON COLUMN skills.status IS '1:正常,3:废弃（skill 无端点；不设「锁定」）';
CREATE TABLE svc_auth_binding_targets (
    binding_id uuid NOT NULL,
    target_type character varying(16) NOT NULL,
    target_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT svc_auth_binding_targets_type_ck CHECK (((target_type)::text = ANY ((ARRAY['agent'::character varying, 'flow'::character varying])::text[])))
);
CREATE TABLE svc_auth_bindings (
    id uuid NOT NULL,
    auth_type character varying(16) NOT NULL,
    external_issuer character varying(300) DEFAULT ''::character varying NOT NULL,
    external_subject character varying(300) NOT NULL,
    user_id uuid NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    restrict_targets boolean DEFAULT false NOT NULL,
    exclude_supervisor boolean DEFAULT false NOT NULL,
    CONSTRAINT svc_auth_bindings_type_ck CHECK (((auth_type)::text = ANY (ARRAY[('oauth'::character varying)::text, ('apikey'::character varying)::text])))
);
COMMENT ON COLUMN svc_auth_bindings.restrict_targets IS '是否限制该凭证可调用的端点目标：false=不限制（默认/存量行为）；true 时按 svc_auth_binding_targets 列出的目标，空集合表示一个都不允许';
COMMENT ON COLUMN svc_auth_bindings.exclude_supervisor IS '仅 restrict_targets=false 时生效：true=放行全部已发布端点但排除超级智能体（supervisor 端点等于对外开放租户管理能力）';
CREATE TABLE svc_endpoints (
    id uuid CONSTRAINT agent_services_id_not_null NOT NULL,
    name character varying CONSTRAINT agent_services_name_not_null NOT NULL,
    created_at timestamp with time zone,
    description character varying CONSTRAINT agent_services_description_not_null NOT NULL,
    target_id uuid,
    target_type character varying,
    protocol character varying,
    exposed boolean DEFAULT false,
    updated_at timestamp with time zone,
    input_schema jsonb DEFAULT '{}'::jsonb
);
CREATE TABLE tool_group_items (
    group_id uuid NOT NULL,
    item_type character varying(8) NOT NULL,
    ref character varying(160) NOT NULL,
    CONSTRAINT tool_group_items_no_nested_group CHECK ((((item_type)::text <> 'group'::text) OR ((ref)::text !~ '^group:'::text))),
    CONSTRAINT tool_group_items_type_check CHECK (((item_type)::text = ANY ((ARRAY['group'::character varying, 'tool'::character varying])::text[])))
);
CREATE TABLE tool_groups (
    id uuid NOT NULL,
    name character varying(64) NOT NULL,
    description text,
    created_by uuid,
    updated_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE users (
    name character varying CONSTRAINT users_name_not_null NOT NULL,
    password character varying,
    display_name character varying,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    email character varying,
    phone character varying,
    id uuid CONSTRAINT users_id_not_null NOT NULL,
    role character varying(16) DEFAULT 'service'::character varying NOT NULL,
    status character varying(16) DEFAULT 'active'::character varying NOT NULL,
    CONSTRAINT users_role_check CHECK (((role)::text = ANY ((ARRAY['manage'::character varying, 'service'::character varying])::text[]))),
    CONSTRAINT users_status_check CHECK (((status)::text = ANY ((ARRAY['active'::character varying, 'disabled'::character varying, 'locked'::character varying])::text[])))
);
ALTER TABLE ONLY agent_relations
    ADD CONSTRAINT agent_relations_pkey PRIMARY KEY (caller_id, callee_id);
ALTER TABLE ONLY svc_endpoints
    ADD CONSTRAINT agent_services_pk PRIMARY KEY (id);
ALTER TABLE ONLY agent_skill_bindings
    ADD CONSTRAINT agent_skill_bindings_pkey1 PRIMARY KEY (agent_id, skill_id);
ALTER TABLE ONLY skills
    ADD CONSTRAINT agent_skills_pkey PRIMARY KEY (id);
ALTER TABLE ONLY agent_tool_grants
    ADD CONSTRAINT agent_tool_grants_pkey PRIMARY KEY (agent_id, grant_kind, item_type, ref);
ALTER TABLE ONLY agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);
ALTER TABLE ONLY llm_models
    ADD CONSTRAINT llm_models_unique UNIQUE (name);
ALTER TABLE ONLY llm_providers
    ADD CONSTRAINT llm_providers_unique UNIQUE (name);
ALTER TABLE ONLY llm_models
    ADD CONSTRAINT llm_source_models_pk PRIMARY KEY (id);
ALTER TABLE ONLY llm_providers
    ADD CONSTRAINT llm_sources_pk PRIMARY KEY (id);
ALTER TABLE ONLY mcp_connection_tools
    ADD CONSTRAINT mcp_connection_tools_conn_name UNIQUE (connection_id, name);
ALTER TABLE ONLY mcp_connection_tools
    ADD CONSTRAINT mcp_connection_tools_pkey PRIMARY KEY (id);
ALTER TABLE ONLY mcp_connections
    ADD CONSTRAINT mcp_connections_name_key UNIQUE (name);
ALTER TABLE ONLY mcp_connections
    ADD CONSTRAINT mcp_connections_pkey PRIMARY KEY (id);
ALTER TABLE ONLY mcp_server_configs
    ADD CONSTRAINT mcp_server_configs_pk PRIMARY KEY (id);
ALTER TABLE ONLY mcp_server_configs
    ADD CONSTRAINT mcp_server_configs_unique UNIQUE (auth_type);
ALTER TABLE ONLY flow_breakpoints
    ADD CONSTRAINT pipe_breakpoints_pkey PRIMARY KEY (flow_id, node_id);
ALTER TABLE ONLY flow_node_records
    ADD CONSTRAINT pipe_node_records_pk PRIMARY KEY (id);
ALTER TABLE ONLY flow_run_records
    ADD CONSTRAINT pipe_run_records_pk PRIMARY KEY (id);
ALTER TABLE ONLY agent_run_records
    ADD CONSTRAINT agent_run_records_pk PRIMARY KEY (id);
ALTER TABLE ONLY session_records
    ADD CONSTRAINT pipe_session_memory_pkey PRIMARY KEY (id);
ALTER TABLE ONLY plugins
    ADD CONSTRAINT plugins_kind_name_key UNIQUE (kind, name);
ALTER TABLE ONLY plugins
    ADD CONSTRAINT plugins_pkey PRIMARY KEY (id);
ALTER TABLE ONLY rpc_connection_tools
    ADD CONSTRAINT rpc_connection_tools_conn_name UNIQUE (connection_id, name);
ALTER TABLE ONLY rpc_connection_tools
    ADD CONSTRAINT rpc_connection_tools_pkey PRIMARY KEY (id);
ALTER TABLE ONLY rpc_connections
    ADD CONSTRAINT rpc_connections_name_key UNIQUE (name);
ALTER TABLE ONLY rpc_connections
    ADD CONSTRAINT rpc_connections_pkey PRIMARY KEY (id);
ALTER TABLE ONLY rpc_server_configs
    ADD CONSTRAINT rpc_server_configs_pk PRIMARY KEY (id);
ALTER TABLE ONLY rpc_server_configs
    ADD CONSTRAINT rpc_server_configs_unique UNIQUE (auth_type);
ALTER TABLE ONLY custom_tools
    ADD CONSTRAINT custom_tools_name_key UNIQUE (name);
ALTER TABLE ONLY custom_tools
    ADD CONSTRAINT custom_tools_pkey PRIMARY KEY (id);
ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY skill_tool_grants
    ADD CONSTRAINT skill_tool_grants_pkey PRIMARY KEY (skill_id, item_type, ref);
ALTER TABLE ONLY svc_auth_binding_targets
    ADD CONSTRAINT svc_auth_binding_targets_pkey PRIMARY KEY (binding_id, target_type, target_id);
ALTER TABLE ONLY svc_auth_bindings
    ADD CONSTRAINT svc_auth_bindings_pkey PRIMARY KEY (id);
ALTER TABLE ONLY svc_auth_bindings
    ADD CONSTRAINT svc_auth_bindings_unique UNIQUE (auth_type, external_issuer, external_subject);
ALTER TABLE ONLY svc_endpoints
    ADD CONSTRAINT svc_endpoints_unique UNIQUE (protocol, target_type, target_id);
ALTER TABLE ONLY svc_endpoints
    ADD CONSTRAINT svc_endpoints_unique_1 UNIQUE (name, protocol);
ALTER TABLE ONLY tool_group_items
    ADD CONSTRAINT tool_group_items_pkey PRIMARY KEY (group_id, item_type, ref);
ALTER TABLE ONLY tool_groups
    ADD CONSTRAINT tool_groups_name_uniq UNIQUE (name);
ALTER TABLE ONLY tool_groups
    ADD CONSTRAINT tool_groups_pkey PRIMARY KEY (id);
ALTER TABLE ONLY agents
    ADD CONSTRAINT uk_agents_name UNIQUE (name);
ALTER TABLE ONLY users
    ADD CONSTRAINT users_name_key UNIQUE (name);
ALTER TABLE ONLY users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);
ALTER TABLE ONLY flows
    ADD CONSTRAINT wf_instances_pk PRIMARY KEY (id);
ALTER TABLE ONLY flows
    ADD CONSTRAINT wf_instances_unique UNIQUE (name);
CREATE INDEX agent_tool_grants_ref_idx ON agent_tool_grants USING btree (ref);
CREATE INDEX idx_agent_run_records_agent ON agent_run_records USING btree (agent_id, started_at DESC);
CREATE INDEX idx_agent_run_records_session ON agent_run_records USING btree (session_id, started_at DESC);
CREATE INDEX idx_agent_skills_agent_id ON skills USING btree (agent_id);
CREATE INDEX idx_agents_status ON agents USING btree (status);
CREATE INDEX idx_pipe_node_records_run_status ON flow_node_records USING btree (run_id, status);
CREATE INDEX idx_pipe_run_records_pipe ON flow_run_records USING btree (flow_id, started_at DESC);
CREATE INDEX idx_pipe_run_records_user ON flow_run_records USING btree (user_id, started_at DESC);
CREATE INDEX idx_pipe_run_records_session ON flow_run_records USING btree (session_id);
CREATE INDEX idx_pipe_session_memory ON session_records USING btree (owner_id, session_id, created_at DESC);
CREATE INDEX idx_sessions_owner ON sessions USING btree (owner_type, owner_id, updated_at DESC);
CREATE INDEX idx_sessions_user ON sessions USING btree (owner_type, owner_id, user_id, updated_at DESC);
CREATE INDEX idx_sessions_user_latest ON sessions USING btree (owner_type, user_id, updated_at DESC);
CREATE INDEX idx_svc_auth_binding_targets_target ON svc_auth_binding_targets USING btree (target_type, target_id);
CREATE INDEX idx_svc_auth_bindings_user ON svc_auth_bindings USING btree (user_id);
CREATE INDEX idx_tenant_agent_skills_created_by ON skills USING btree (created_by);
CREATE INDEX idx_tenant_agent_skills_name ON skills USING btree (name);
CREATE INDEX custom_tools_status_idx ON custom_tools USING btree (status);
CREATE INDEX skill_tool_grants_ref_idx ON skill_tool_grants USING btree (ref);
CREATE INDEX t0_agent_relations_callee_idx ON agent_relations USING btree (callee_id);
CREATE INDEX t0_agent_skill_bindings_skill_idx ON agent_skill_bindings USING btree (skill_id);
CREATE INDEX tool_group_items_ref_idx ON tool_group_items USING btree (ref);
CREATE UNIQUE INDEX uk_agent_skills_agent_name ON skills USING btree (agent_id, name) WHERE (agent_id IS NOT NULL);
CREATE UNIQUE INDEX uk_agent_skills_tenant_name ON skills USING btree (name) WHERE (agent_id IS NULL);
CREATE UNIQUE INDEX users_display_name_idx ON users USING btree (display_name);
ALTER TABLE ONLY agent_run_records
    ADD CONSTRAINT agent_run_records_agent_fk FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE ONLY agent_relations
    ADD CONSTRAINT agent_relations_callee_fk FOREIGN KEY (callee_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE ONLY agent_relations
    ADD CONSTRAINT agent_relations_caller_fk FOREIGN KEY (caller_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE ONLY agent_skill_bindings
    ADD CONSTRAINT agent_skill_bindings_agent_fk FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE ONLY agent_skill_bindings
    ADD CONSTRAINT agent_skill_bindings_skill_fk FOREIGN KEY (skill_id) REFERENCES skills(id) ON DELETE CASCADE;
ALTER TABLE ONLY agent_tool_grants
    ADD CONSTRAINT agent_tool_grants_agent_fk FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE ONLY mcp_connection_tools
    ADD CONSTRAINT mcp_connection_tools_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES mcp_connections(id) ON DELETE CASCADE;
ALTER TABLE ONLY rpc_connection_tools
    ADD CONSTRAINT rpc_connection_tools_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES rpc_connections(id) ON DELETE CASCADE;
ALTER TABLE ONLY skill_tool_grants
    ADD CONSTRAINT skill_tool_grants_skill_fk FOREIGN KEY (skill_id) REFERENCES skills(id) ON DELETE CASCADE;
ALTER TABLE ONLY svc_auth_binding_targets
    ADD CONSTRAINT svc_auth_binding_targets_binding_id_fkey FOREIGN KEY (binding_id) REFERENCES svc_auth_bindings(id) ON DELETE CASCADE;
ALTER TABLE ONLY tool_group_items
    ADD CONSTRAINT tool_group_items_group_id_fkey FOREIGN KEY (group_id) REFERENCES tool_groups(id) ON DELETE CASCADE;

-- 版本表的四个外键刻意放在这里、而不是写在列上（列内联的写法在空库上会让整份脚本失败）：
-- 文件按 pg_dump 的顺序执行 —— 先建全部表，最后统一加主键与外键。若把 REFERENCES 写进 CREATE TABLE，
-- 此时被引用表（flows / agents / skills / custom_tools）的 PRIMARY KEY 还没加，PostgreSQL 会直接报
-- 42830 (no unique constraint matching given keys)，新增租户就卡在「执行模板建表」这一步。
ALTER TABLE ONLY flow_versions
    ADD CONSTRAINT flow_versions_flow_id_fkey FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE;
ALTER TABLE ONLY agent_versions
    ADD CONSTRAINT agent_versions_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE ONLY skill_versions
    ADD CONSTRAINT skill_versions_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES skills(id) ON DELETE CASCADE;
ALTER TABLE ONLY tool_versions
    ADD CONSTRAINT tool_versions_tool_id_fkey FOREIGN KEY (tool_id) REFERENCES custom_tools(id) ON DELETE CASCADE;