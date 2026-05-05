-- Reverse 0001_init.up.sql.
--
-- Drop in reverse order to avoid foreign-key issues (rate_limit_policies
-- references clients via FK).

DROP TABLE IF EXISTS request_logs;
DROP TABLE IF EXISTS rate_limit_policies;
DROP TABLE IF EXISTS upstream_routes;
DROP TABLE IF EXISTS clients;-- Reverse 0001_init.up.sql.
--
-- Drop in reverse order to avoid foreign-key issues (rate_limit_policies
-- references clients via FK).

DROP TABLE IF EXISTS request_logs;
DROP TABLE IF EXISTS rate_limit_policies;
DROP TABLE IF EXISTS upstream_routes;
DROP TABLE IF EXISTS clients;
