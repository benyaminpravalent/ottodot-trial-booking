-- Runs once on first container start. POSTGRES_DB already created `ottodot`;
-- add the dedicated test database so `go test` never touches dev data.
CREATE DATABASE ottodot_test;
