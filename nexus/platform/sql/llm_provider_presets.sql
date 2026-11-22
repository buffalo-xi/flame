-- ============================================================================
-- platform.llm_provider_presets: globally shared LLM provider presets
--
-- Why this file exists: the table had no CREATE TABLE anywhere in this repository (same class of defect as
-- platform.flow_configs), so a database provisioned from the SQL files lacked it and the system settings page returned
-- a generic "internal error" instead of the preset list.
--
-- Presets are read-only for tenants: picking one on the tenant side copies name / protocol / url into that tenant's own
-- provider record, so there is no foreign key pointing here.
--
-- Idempotent: safe to re-run. deploy/poc/entrypoint.sh applies the platform SQL on every boot.
-- ============================================================================
CREATE TABLE IF NOT EXISTS platform.llm_provider_presets (
    id           uuid        PRIMARY KEY,           -- generated in the program, no database default
    name         varchar(64) NOT NULL,
    icon         text        NULL,                  -- empty icon is written as NULL
    description  text        NULL,
    protocol     varchar(32) NOT NULL,              -- openai / anthropic / ...
    url          text        NOT NULL,
    is_popular   boolean     NOT NULL DEFAULT false,
    sort_order   int         NOT NULL DEFAULT 0,
    extra_params jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Matches the list order: popular first, then the explicit sort order.
CREATE INDEX IF NOT EXISTS llm_provider_presets_order_idx
    ON platform.llm_provider_presets (is_popular DESC, sort_order);
