-- ============================================================================
-- Agent default output language: agents.default_language + agent_versions.default_language
--
-- Idempotent: ADD COLUMN IF NOT EXISTS, looped over every tenant schema.
--
-- Why both tables: the language is part of the definition, so a release freezes it into the version row and
-- production reads the version's copy (versioning.AgentCurrentAttributes) instead of the draft. NULL = not
-- configured (the caller's _meta.language wins, then the input's own language, then the product default).
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
        EXECUTE format($f$
            ALTER TABLE %I.agents
                ADD COLUMN IF NOT EXISTS default_language varchar(35)
        $f$, s);

        EXECUTE format($f$
            ALTER TABLE %I.agent_versions
                ADD COLUMN IF NOT EXISTS default_language varchar(35)
        $f$, s);
    END LOOP;
END $$;
