DROP INDEX IF EXISTS idx_assigned_port_unique;
ALTER TABLE projects DROP COLUMN assigned_port;
ALTER TABLE projects DROP COLUMN opencode_project_id;
