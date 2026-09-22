ALTER TABLE agent_runtime DROP COLUMN active_provider_preset_id;

DROP INDEX IF EXISTS idx_provider_preset_workspace_runtime_type;
DROP INDEX IF EXISTS idx_provider_preset_workspace;
DROP TABLE IF EXISTS provider_preset;
