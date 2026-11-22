-- ============================================================================
-- Default LLM provider presets (platform.llm_provider_presets)
--
-- Why seed at all: this table is the source of the provider list the console offers when someone adds an LLM provider.
-- An empty table is not an error - it renders as "no presets" - so the gap was invisible until looked for, but it leaves
-- a fresh deployment with nothing to pick. These six are the providers the product is verified against; the icons are
-- served by nexus from nexus/static/llm/ (see the /static route), not by the desktop bundle.
--
-- Idempotent: a row is inserted only when no preset with that name exists, so an operator's edits survive a restart
-- (same rule as deploy/poc/postgres/90-admin.sql). The trade-off is symmetric: deleting one of these rows from the
-- console brings it back on the next boot - rename it instead of deleting it if it must stay gone.
--
-- Ids are fixed rather than generated so that re-running against a database that already has the rows is a no-op and
-- two environments end up with the same ids.
-- ============================================================================
INSERT INTO platform.llm_provider_presets (id, name, icon, description, protocol, url, is_popular, sort_order)
SELECT 'ee802aa9-5a15-45cb-ab69-bc743d01ed30', 'openai_compatible', '/static/llm/compatible.png', NULL,
       'openai', 'http://host:port/v1', true, 50
 WHERE NOT EXISTS (SELECT 1 FROM platform.llm_provider_presets WHERE name = 'openai_compatible');

INSERT INTO platform.llm_provider_presets (id, name, icon, description, protocol, url, is_popular, sort_order)
SELECT 'bd5f463e-698e-493c-81d3-47cb5443e2fc', 'ollama', '/static/llm/ollama.jpg', NULL,
       'openai', 'http://localhost:11434/v1', false, 50
 WHERE NOT EXISTS (SELECT 1 FROM platform.llm_provider_presets WHERE name = 'ollama');

INSERT INTO platform.llm_provider_presets (id, name, icon, description, protocol, url, is_popular, sort_order)
SELECT 'c9967aaa-68c3-42ce-9dcb-00c84be2e493', 'vertexai', '/static/llm/vertexai.jpg', NULL,
       'vertexai', 'https://us-central1-aiplatform.googleapis.com/v1', false, 80
 WHERE NOT EXISTS (SELECT 1 FROM platform.llm_provider_presets WHERE name = 'vertexai');

INSERT INTO platform.llm_provider_presets (id, name, icon, description, protocol, url, is_popular, sort_order)
SELECT 'c6fb39a6-7d24-4edc-88ca-922f7ddde254', 'googleai', '/static/llm/googleai.png', NULL,
       'googleai', 'https://generativelanguage.googleapis.com/v1beta', false, 85
 WHERE NOT EXISTS (SELECT 1 FROM platform.llm_provider_presets WHERE name = 'googleai');

INSERT INTO platform.llm_provider_presets (id, name, icon, description, protocol, url, is_popular, sort_order)
SELECT 'fb24e4be-9490-404e-9049-5da63a953439', 'anthropic', '/static/llm/anthropic.png', NULL,
       'anthropic', 'https://api.anthropic.com/v1', false, 90
 WHERE NOT EXISTS (SELECT 1 FROM platform.llm_provider_presets WHERE name = 'anthropic');

INSERT INTO platform.llm_provider_presets (id, name, icon, description, protocol, url, is_popular, sort_order)
SELECT 'add648ca-42b3-4757-99bd-f737825be6ca', 'openai', '/static/llm/openai.png', NULL,
       'openai', 'https://api.openai.com/v1', false, 100
 WHERE NOT EXISTS (SELECT 1 FROM platform.llm_provider_presets WHERE name = 'openai');
