-- Runs once, and only when the PostgreSQL data directory is empty (the official image's rule for
-- /docker-entrypoint-initdb.d). Repeat runs on an existing volume are therefore not a scenario - the
-- idempotent form below is about not failing when the file is applied by hand on a database that already
-- has the database.
--
-- There is no "create the role, hand over ownership" step: POSTGRES_USER=flame makes initdb create exactly
-- that role with POSTGRES_PASSWORD, and POSTGRES_DB=flame makes it the owner of the flame database. All the
-- second database needs is to exist before the LiteLLM container starts retrying.
--
-- LiteLLM creates its own tables (Prisma, on first start); nothing here touches its schema.
SELECT 'CREATE DATABASE litellm OWNER flame'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'litellm')\gexec
