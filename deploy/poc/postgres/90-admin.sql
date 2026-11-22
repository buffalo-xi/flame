-- The platform administrator for the demo box.
--
-- nexus creates no account on startup (by design), so the first administrator has to come from seed data. This is that
-- seed data — and it is the only reason this file exists.
--
-- Login:    plat
-- Password: 12345678
-- Hash:     bcrypt, cost 10, produced by the same call (`bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)`)
--           as nexus/shared/account.HashPassword. Verified against the plaintext before being committed here.
--
-- ⚠️ PoC only. `plat` is a protected platform account (its profile, role and status cannot be changed), the password is
-- baked into the image and cannot be rotated from the console. Do not reuse this image anywhere the port is reachable
-- from an untrusted network: change the hash below and rebuild, or the demo box is a shared-password box.
--
-- Idempotent: re-applied on every boot, and it only inserts when the row is missing. A password changed by hand in the
-- database therefore survives restarts.
INSERT INTO platform.users (id, name, display_name, password, role, status, created_at, updated_at)
SELECT gen_random_uuid(), 'plat', 'platform administrator',
       '$2a$10$le.uWoQ28GWuIAqxoLC1Z.urqVBaVbh7FWWmPpDJa.0KfXAyMMiw.',
       'admin', 'active', NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM platform.users WHERE name = 'plat');
