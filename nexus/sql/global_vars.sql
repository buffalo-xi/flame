-- ============================================================================
-- 全局变量：建表（存量租户）
--
-- 背景：资产（工作流 / 智能体 / 技能）导出导入时，值被写死在定义里 —— 要么
--   跨租户泄漏，要么上架时被 StripSecrets 清空后要人肉重填一遍。全局变量把
--   「值」从资产里搬出来，资产只留引用名，目标租户填一次值，所有资产同时生效。
--
-- 作用域：本租户内全局（每租户一个 schema，天然隔离），**不跨租户**。
--   与 flows.env_vars（环境变量，单流程）、flows.flow_vars（流程变量，运行期可变）
--   同属 <scope>_vars 一族。
--
-- 引用方式（代码侧）：
--   1) 工作流：节点 config.inputs 里加一项 { "source_type": "global", "source_key": "变量名" }，
--      模板里写 {{ .input.变量名 }}。没有模板级的 {{ .global_vars.X }}，也没有
--      「环境变量指向全局变量」的 { "type": "global", ... } —— 两者都已下线，前者载入即报错，
--      后者运行期报「已废弃的类型 global」。
--   2) 智能体 / 技能文档：${global_vars.变量名}（见 shared/globalvars/expand.go）
--   3) 自定义工具：引用不到，租户常量走工具自己的 config_fields（见 custom-tool-authoring 技能）
--
-- 加密：kind=secret 的值由服务端加密后落库（enc:v1:...，AES-256-GCM）。
--   本脚本只建表，不碰数据 —— 加密功能上线前写入的明文行会在下一次「保存」时
--   自动转成密文（读路径不写库）。查还剩哪些明文见下面「执行后核对 c」。
--
-- 幂等：可重复执行（IF NOT EXISTS）。
-- 新建租户走 platform/provision/template/schema.sql，不需要本脚本。
--
-- 判定「哪些 schema 是租户」：用 flows 表是否存在（每个租户都有，platform 没有）。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flows'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($ddl$
            CREATE TABLE IF NOT EXISTS %I.global_vars (
                name character varying(64) NOT NULL,
                value text DEFAULT ''::text NOT NULL,
                kind character varying(16) DEFAULT 'text'::character varying NOT NULL,
                description character varying(200) DEFAULT ''::character varying NOT NULL,
                created_by uuid,
                updated_by uuid,
                created_at timestamp with time zone DEFAULT now() NOT NULL,
                updated_at timestamp with time zone DEFAULT now() NOT NULL,
                CONSTRAINT global_vars_pkey PRIMARY KEY (name)
            )$ddl$, s);

        EXECUTE format(
            'COMMENT ON TABLE %I.global_vars IS %L', s,
            '全局变量：本租户内全局，值不出租户、不进资产包');
        EXECUTE format(
            'COMMENT ON COLUMN %I.global_vars.kind IS %L', s,
            'text / secret（接口掩码 + 落库加密、不应在提示词里引用）/ number / bool');
        EXECUTE format(
            'COMMENT ON COLUMN %I.global_vars.description IS %L', s,
            '给人看的用途说明；导出资产包时随需求清单一起带出');
    END LOOP;
END $$;

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 每个租户 schema 都有了这张表
--   SELECT table_schema FROM information_schema.tables
--    WHERE table_name = 'global_vars' ORDER BY 1;
--
-- b) 表结构
--   \d t0.global_vars
--
-- c) 还有哪些 secret 是明文（加密上线前的存量，重存一次即转密文）
--   SELECT name FROM t0.global_vars
--    WHERE kind = 'secret' AND value <> '' AND value NOT LIKE 'enc:v1:%';
--
-- ============================================================================
-- 回滚
-- ============================================================================
-- BEGIN;
-- DROP TABLE IF EXISTS t0.global_vars;
-- COMMIT;
