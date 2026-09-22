-- Provider presets (runtime-level supplier configuration). See migration 502
-- for the table and the rationale for server-side delivery.
--
-- Relational integrity is enforced in the application layer — there are no DB
-- FKs, matching migration 120's house rule. Whatever a preset is bound to is
-- cleaned up by the delete handler, never by ON DELETE CASCADE.

-- name: CreateProviderPreset :one
INSERT INTO provider_preset (
    workspace_id,
    name,
    runtime_type,
    protocol_family,
    env,
    model,
    thinking_level,
    native_config,
    visibility,
    created_by,
    enabled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetProviderPresetForWorkspace :one
SELECT * FROM provider_preset
WHERE id = $1 AND workspace_id = $2;

-- name: ListProviderPresets :many
SELECT * FROM provider_preset
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ListProviderPresetsByRuntimeType :many
-- The "apply to this runtime" picker: only presets authored for the runtime's
-- own backend are candidates, because env keys, native_config shape and model
-- namespace are all family-specific.
SELECT * FROM provider_preset
WHERE workspace_id = $1 AND runtime_type = $2
ORDER BY created_at ASC;

-- name: UpdateProviderPreset :one
-- Partial update via COALESCE: NULL args leave the column unchanged.
-- runtime_type and protocol_family are intentionally NOT updatable — see the
-- migration comment on provider_preset.runtime_type.
UPDATE provider_preset
SET name           = COALESCE(sqlc.narg('name'), name),
    env            = COALESCE(sqlc.narg('env'), env),
    model          = COALESCE(sqlc.narg('model'), model),
    thinking_level = COALESCE(sqlc.narg('thinking_level'), thinking_level),
    native_config  = COALESCE(sqlc.narg('native_config'), native_config),
    enabled        = COALESCE(sqlc.narg('enabled'), enabled),
    updated_at     = now()
WHERE id = @id AND workspace_id = @workspace_id
RETURNING *;

-- name: DeleteProviderPreset :exec
DELETE FROM provider_preset
WHERE id = $1 AND workspace_id = $2;

-- name: SetRuntimeProviderPreset :one
-- The "sync to this runtime" write. @preset_id may be NULL, which clears the
-- runtime back to "every agent uses its own configuration".
UPDATE agent_runtime
SET active_provider_preset_id = @preset_id, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: ClearProviderPresetFromRuntimes :many
-- Application-layer cleanup for preset deletion: migration 502 added no DB FK,
-- so the runtimes still pointing at a deleted preset are unbound here. Returns
-- the affected runtime ids so the caller can broadcast a refresh.
UPDATE agent_runtime
SET active_provider_preset_id = NULL, updated_at = now()
WHERE workspace_id = @workspace_id AND active_provider_preset_id = @preset_id
RETURNING id;

-- name: GetActiveProviderPresetForRuntime :one
-- Runtime -> preset resolution for the daemon claim path. Disabled presets are
-- treated as not applied at all, so flipping enabled=false takes effect on the
-- next task without touching the runtime row. ErrNoRows means "no preset in
-- force", which the caller treats as a no-op rather than an error.
SELECT p.* FROM provider_preset p
JOIN agent_runtime ar ON ar.active_provider_preset_id = p.id
WHERE ar.id = @runtime_id AND p.enabled = true;

-- name: ListActiveProviderPresetsForRuntimes :many
-- Batch sibling of GetActiveProviderPresetForRuntime, for the agent list: one
-- round trip for every runtime in the response instead of one per agent. Only
-- the columns the override disclosure needs are selected — never the preset's
-- identity beyond its name.
--
-- Disabled presets are treated as not applied, matching the single-runtime
-- query: flipping enabled=false must stop the override everywhere at once.
SELECT ar.id AS runtime_id, p.id, p.name, p.env, p.model, p.thinking_level
FROM provider_preset p
JOIN agent_runtime ar ON ar.active_provider_preset_id = p.id
WHERE ar.id = ANY(@runtime_ids::uuid[]) AND p.enabled = true;
