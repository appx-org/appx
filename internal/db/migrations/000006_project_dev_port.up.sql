ALTER TABLE projects ADD COLUMN dev_port INTEGER;
CREATE UNIQUE INDEX idx_dev_port_unique ON projects(dev_port) WHERE dev_port IS NOT NULL;
