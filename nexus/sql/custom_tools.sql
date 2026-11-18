-- ============================================================================
-- custom_tools：租户自己写的自定义工具（代码在盘上，本表只记身份与协议）
-- ----------------------------------------------------------------------------
-- 与 plugins 的分工：
--   plugins        磁盘上发现的**实现**（Python 插件，一份代码服务所有租户）
--   custom_tools   租户自己写的**工具**（一段脚本 + 入参 schema，只属于本租户）
--
-- 与内置工具的分工：内置工具在代码里（tools.Register），这里的是运行期数据。
--
-- 为什么代码不放表里：脚本是「定义域」内容，和 SKILL.md / AGENT.md 一个性质 ——
-- 放盘上才能被文件工具编辑、被 git 版本化。表里只存元数据：
--   name          工具短名（对外暴露为 custom__<name>，ref 为 custom:<name>）
--   parameters    入参 JSON Schema（**发给模型**，决定它怎么填参数）
--   output        出参 JSON Schema（**不发模型**，只用于运行时校验与面板展示）
--   config_fields 配置项声明（管理端据此渲染表单、决定哪几项打码）
--   configs       配置值（**明文**，与 plugins.configs 同口径；打码只发生在回显）
--
-- 生效语义（与工具授权叠加）：
--   status=1 且被 agent_tool_grants 授权 且 平台开通 sandbox.exec  → 可执行
--   任一不满足 → 目录置灰（原因分开），执行层直接拒。
--   能力档是 exec 不是 script：自定义工具走 /v1/exec（一次性进程、无池），
--   sandbox.code 是 /v1/code 通道（工作流 code 节点）的闸。
--
-- 路径：代码在 {root_dir}/tenants/{schema}/tools/{name}/{entry}
-- 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($ddl$
            CREATE TABLE IF NOT EXISTS %I.custom_tools (
                id uuid NOT NULL,
                name varchar(64) NOT NULL,
                display_name varchar(64) DEFAULT ''::varchar NOT NULL,
                description text DEFAULT ''::text NOT NULL,
                parameters jsonb DEFAULT '{}'::jsonb NOT NULL,
                output jsonb,
                lang varchar(16) NOT NULL,
                entry varchar(128) DEFAULT 'main.py'::varchar NOT NULL,
                timeout_ms integer DEFAULT 60000 NOT NULL,
                max_output_bytes integer DEFAULT 65536 NOT NULL,
                egress boolean DEFAULT false NOT NULL,
                config_fields jsonb DEFAULT '[]'::jsonb NOT NULL,
                configs jsonb DEFAULT '{}'::jsonb NOT NULL,
                status integer DEFAULT 1 NOT NULL,
                created_by varchar(64) DEFAULT ''::varchar NOT NULL,
                created_at timestamptz DEFAULT now() NOT NULL,
                updated_at timestamptz DEFAULT now() NOT NULL
            )$ddl$, s);

        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'custom_tools' AND c.contype = 'p'
        ) THEN
            EXECUTE format('ALTER TABLE %I.custom_tools ADD CONSTRAINT custom_tools_pkey PRIMARY KEY (id)', s);
        END IF;

        -- 名字在租户内唯一：对外工具名 custom__<name> 也靠它去重
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'custom_tools' AND c.contype = 'u'
        ) THEN
            EXECUTE format('ALTER TABLE %I.custom_tools ADD CONSTRAINT custom_tools_name_key UNIQUE (name)', s);
        END IF;

        EXECUTE format('CREATE INDEX IF NOT EXISTS custom_tools_status_idx ON %I.custom_tools (status)', s);
    END LOOP;
END $$;

-- 验证
SELECT table_schema, table_name
  FROM information_schema.tables
 WHERE table_name = 'custom_tools'
 ORDER BY table_schema;
