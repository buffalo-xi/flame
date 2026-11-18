-- ============================================================================
-- 工具授权体系：agent 工具授权 / skill 工具依赖 / 自定义工具组
--
-- 幂等：可重复执行；按租户 schema 循环（与 agents / skills 同库，逐租户建表）
--
-- 设计要点：
--   1) 天然组（builtin:file|exec|agent|skill|super、mcp:<conn>、rpc:<conn>、local:rpc）
--      与预设组都不落库，由代码 + 连接表 + 端点表动态生成；
--   2) 自定义组（tool_groups）成员只能是「天然组」或「单个工具」，不允许嵌套自定义组；
--   3) agent 侧 grant_kind 三态：
--        required 必须导入（恒注入，不受 deny 影响）
--        allow    可使用（被 deny 覆盖）
--        deny     显式拒绝（只对 allow 生效）
--   4) skill 侧（skill_tool_grants）只声明「需要」哪些工具，不产生授权
--      —— 防止 agent 自建 skill 提权；
--   5) 按需注入：final = required ∪ { skill 需要且 allow 的工具 }；
--      skill 依赖尚未录入（候选为空）时回落为 required ∪ allow（等价「授权即生效」）；
--   6) ref 语法统一为 type:... ，例如 builtin:read_file / mcp:fs:read_file / local:rpc:weather；
--   7) 本地端点只开放 protocol='rpc'（local:rpc 组 / local:rpc:<endpointName>），
--      且只收 target_type='flow' 的端点 —— agent 端点走 call_agent（agent_relations 等闸门），
--      不进工具目录（详见 toolgrant.LocalTargetFlow）。
-- ============================================================================

DO $$
DECLARE s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1) agents 增加「不限制工具」开关（开启后 skill 声明的工具都可接受）
        EXECUTE format($f$
            ALTER TABLE %I.agents
                ADD COLUMN IF NOT EXISTS tools_unrestricted bool NOT NULL DEFAULT false
        $f$, s);

        -- 1b) agents 增加「可发现未绑定的技能」开关
        --     默认 false（严格）：agent 只看得见自己的私有 skill + 被绑定到它的 skill；
        --     开启后额外可通过 list_skills 发现「全员可见（restricted=false）」的共享 skill。
        --     注意：system prompt 的技能清单始终只列「私有 + 已绑定」，与这个开关无关。
        EXECUTE format($f$
            ALTER TABLE %I.agents
                ADD COLUMN IF NOT EXISTS skill_discovery bool NOT NULL DEFAULT false
        $f$, s);

        -- 2) 自定义工具组
        EXECUTE format($f$
            CREATE TABLE IF NOT EXISTS %I.tool_groups (
                id          uuid        PRIMARY KEY,
                name        varchar(64) NOT NULL,
                description text        NULL,
                created_by  uuid        NULL,
                updated_by  uuid        NULL,
                created_at  timestamptz NOT NULL DEFAULT now(),
                updated_at  timestamptz NOT NULL DEFAULT now(),
                CONSTRAINT tool_groups_name_uniq UNIQUE (name)
            )
        $f$, s);

        -- 3) 自定义组成员：天然组 + 单个工具（禁止嵌套自定义组）
        EXECUTE format($f$
            CREATE TABLE IF NOT EXISTS %I.tool_group_items (
                group_id  uuid         NOT NULL REFERENCES %I.tool_groups(id) ON DELETE CASCADE,
                item_type varchar(8)   NOT NULL,
                ref       varchar(160) NOT NULL,
                PRIMARY KEY (group_id, item_type, ref),
                CONSTRAINT tool_group_items_type_check CHECK (item_type IN ('group', 'tool')),
                CONSTRAINT tool_group_items_no_nested_group CHECK (item_type <> 'group' OR ref !~ '^group:')
            )
        $f$, s, s);

        EXECUTE format($f$
            CREATE INDEX IF NOT EXISTS tool_group_items_ref_idx ON %I.tool_group_items (ref)
        $f$, s);

        -- 4) agent 工具授权（required 必须导入 / allow 可使用 / deny 显式拒绝）
        EXECUTE format($f$
            CREATE TABLE IF NOT EXISTS %I.agent_tool_grants (
                agent_id   uuid         NOT NULL,
                grant_kind varchar(8)   NOT NULL,
                item_type  varchar(8)   NOT NULL,
                ref        varchar(160) NOT NULL,
                updated_by uuid         NULL,
                updated_at timestamptz  NOT NULL DEFAULT now(),
                PRIMARY KEY (agent_id, grant_kind, item_type, ref),
                CONSTRAINT agent_tool_grants_kind_check CHECK (grant_kind IN ('required', 'allow', 'deny')),
                CONSTRAINT agent_tool_grants_type_check CHECK (item_type IN ('group', 'tool'))
            )
        $f$, s);

        EXECUTE format($f$
            CREATE INDEX IF NOT EXISTS agent_tool_grants_ref_idx ON %I.agent_tool_grants (ref)
        $f$, s);

        -- 5) skill 工具依赖（只表达「需要」，不产生授权）
        EXECUTE format($f$
            CREATE TABLE IF NOT EXISTS %I.skill_tool_grants (
                skill_id  uuid         NOT NULL,
                item_type varchar(8)   NOT NULL,
                ref       varchar(160) NOT NULL,
                PRIMARY KEY (skill_id, item_type, ref),
                CONSTRAINT skill_tool_grants_type_check CHECK (item_type IN ('group', 'tool'))
            )
        $f$, s);

        EXECUTE format($f$
            CREATE INDEX IF NOT EXISTS skill_tool_grants_ref_idx ON %I.skill_tool_grants (ref)
        $f$, s);

        -- 6) 外键 + ON DELETE CASCADE：把「删 skill / 删 agent」的级联清理交给数据库，
        --    避免任何删除路径漏清授权数据（应用层的手工 DELETE 变为冗余的兜底）。
        --    加约束前先清历史孤儿行，否则 ADD CONSTRAINT 会失败。

        -- 6.1 agent_skill_bindings（由 agent_skill_bindings.sql 建表，这里判存在后处理）
        IF to_regclass(format('%I.agent_skill_bindings', s)) IS NOT NULL THEN
            EXECUTE format($f$
                DELETE FROM %I.agent_skill_bindings b
                 WHERE NOT EXISTS (SELECT 1 FROM %I.skills sk WHERE sk.id = b.skill_id)
                    OR NOT EXISTS (SELECT 1 FROM %I.agents a WHERE a.id = b.agent_id)
            $f$, s, s, s);

            IF NOT EXISTS (SELECT 1 FROM pg_constraint
                            WHERE conname = 'agent_skill_bindings_agent_fk'
                              AND conrelid = to_regclass(format('%I.agent_skill_bindings', s))) THEN
                EXECUTE format($f$
                    ALTER TABLE %I.agent_skill_bindings
                        ADD CONSTRAINT agent_skill_bindings_agent_fk
                        FOREIGN KEY (agent_id) REFERENCES %I.agents(id) ON DELETE CASCADE
                $f$, s, s);
            END IF;

            IF NOT EXISTS (SELECT 1 FROM pg_constraint
                            WHERE conname = 'agent_skill_bindings_skill_fk'
                              AND conrelid = to_regclass(format('%I.agent_skill_bindings', s))) THEN
                EXECUTE format($f$
                    ALTER TABLE %I.agent_skill_bindings
                        ADD CONSTRAINT agent_skill_bindings_skill_fk
                        FOREIGN KEY (skill_id) REFERENCES %I.skills(id) ON DELETE CASCADE
                $f$, s, s);
            END IF;
        END IF;

        -- 6.2 agent_tool_grants（本文件建的 4 号表）
        EXECUTE format($f$
            DELETE FROM %I.agent_tool_grants g
             WHERE NOT EXISTS (SELECT 1 FROM %I.agents a WHERE a.id = g.agent_id)
        $f$, s, s);

        IF NOT EXISTS (SELECT 1 FROM pg_constraint
                        WHERE conname = 'agent_tool_grants_agent_fk'
                          AND conrelid = to_regclass(format('%I.agent_tool_grants', s))) THEN
            EXECUTE format($f$
                ALTER TABLE %I.agent_tool_grants
                    ADD CONSTRAINT agent_tool_grants_agent_fk
                    FOREIGN KEY (agent_id) REFERENCES %I.agents(id) ON DELETE CASCADE
            $f$, s, s);
        END IF;

        -- 6.3 skill_tool_grants（本文件建的 5 号表）
        EXECUTE format($f$
            DELETE FROM %I.skill_tool_grants g
             WHERE NOT EXISTS (SELECT 1 FROM %I.skills sk WHERE sk.id = g.skill_id)
        $f$, s, s);

        IF NOT EXISTS (SELECT 1 FROM pg_constraint
                        WHERE conname = 'skill_tool_grants_skill_fk'
                          AND conrelid = to_regclass(format('%I.skill_tool_grants', s))) THEN
            EXECUTE format($f$
                ALTER TABLE %I.skill_tool_grants
                    ADD CONSTRAINT skill_tool_grants_skill_fk
                    FOREIGN KEY (skill_id) REFERENCES %I.skills(id) ON DELETE CASCADE
            $f$, s, s);
        END IF;

        -- 6.4 agent_relations（由 agent_relations.sql 建表，这里判存在后处理）
        IF to_regclass(format('%I.agent_relations', s)) IS NOT NULL THEN
            EXECUTE format($f$
                DELETE FROM %I.agent_relations r
                 WHERE NOT EXISTS (SELECT 1 FROM %I.agents a WHERE a.id = r.caller_id)
                    OR NOT EXISTS (SELECT 1 FROM %I.agents a WHERE a.id = r.callee_id)
            $f$, s, s, s);

            IF NOT EXISTS (SELECT 1 FROM pg_constraint
                            WHERE conname = 'agent_relations_caller_fk'
                              AND conrelid = to_regclass(format('%I.agent_relations', s))) THEN
                EXECUTE format($f$
                    ALTER TABLE %I.agent_relations
                        ADD CONSTRAINT agent_relations_caller_fk
                        FOREIGN KEY (caller_id) REFERENCES %I.agents(id) ON DELETE CASCADE
                $f$, s, s);
            END IF;

            IF NOT EXISTS (SELECT 1 FROM pg_constraint
                            WHERE conname = 'agent_relations_callee_fk'
                              AND conrelid = to_regclass(format('%I.agent_relations', s))) THEN
                EXECUTE format($f$
                    ALTER TABLE %I.agent_relations
                        ADD CONSTRAINT agent_relations_callee_fk
                        FOREIGN KEY (callee_id) REFERENCES %I.agents(id) ON DELETE CASCADE
                $f$, s, s);
            END IF;
        END IF;
    END LOOP;
END $$;
