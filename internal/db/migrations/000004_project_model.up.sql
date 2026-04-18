ALTER TABLE projects ADD COLUMN assigned_port INTEGER;
ALTER TABLE projects ADD COLUMN opencode_project_id TEXT;
CREATE UNIQUE INDEX idx_assigned_port_unique ON projects(assigned_port) WHERE assigned_port IS NOT NULL;
