-- Provider Presets (runtime-level supplier configuration).
--
-- A provider preset is a workspace-level, reusable bundle of "which supplier
-- this runtime talks to": base URL, API key, model, thinking level and an
-- optional family-native config fragment. Applying one to an agent_runtime
-- (active_provider_preset_id) makes every agent on that runtime inherit it,
-- which is the "switch the whole machine to the relay in one click" move.
--
-- Delivery is server-side by design: the preset travels in the daemon's claim
-- response and is injected into the child process environment at launch. It is
-- never written into the user's global CLI configuration (~/.claude/settings.json
-- and friends), because every Multica task already gets its own workdir and
-- env — a global write would let concurrent tasks overwrite each other, and it
-- would silently reconfigure the user's own machine.
--
-- Referential integrity policy (house rule): this migration adds NO database
-- foreign keys or ON DELETE cascades. workspace_id, created_by and
-- agent_runtime.active_provider_preset_id are plain UUID columns; the
-- relationships they model are enforced in the application layer. Deleting a
-- preset clears the runtimes pointing at it in the delete handler.
--
-- No CHECK constraint on runtime_type, on purpose. migration 120 pinned
-- runtime_profile.protocol_family with one, and the list has since been
-- widened by ten follow-up migrations (134, 136, 175, 179, 202, 242, 253,
-- 254, 313, 342, 370, 403, 441) — every one of them a schema change to keep a
-- duplicated Go whitelist in sync. Presets are validated against the same
-- whitelist in the handler instead, so adding a backend stays a one-file
-- change.

CREATE TABLE provider_preset (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Owning workspace. Plain UUID; integrity (and cleanup on workspace
    -- delete) is enforced in the application layer, not by a DB FK.
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    -- Which backend this preset configures. IMMUTABLE after creation: a
    -- preset's env keys, native_config shape and model namespace are all
    -- family-specific, so repointing an applied preset at another backend
    -- would silently feed one supplier's config to a different CLI.
    runtime_type TEXT NOT NULL,
    -- Derived from runtime_type at write time (agent.RuntimeProtocolFamily).
    -- Stored so the daemon and the model catalog can filter without
    -- re-deriving, and so a legacy runtime_type keeps resolving.
    protocol_family TEXT NOT NULL,
    -- Environment injected into the child process, e.g.
    -- ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY. SECRET: values are masked on
    -- every read path and never leaves the server in the clear — see
    -- handler/provider_preset.go.
    env JSONB NOT NULL DEFAULT '{}',
    -- Runtime-native model id. Empty = inherit (agent model, then the
    -- daemon's own default).
    model TEXT NOT NULL DEFAULT '',
    -- Runtime-native reasoning/effort token. Empty = inherit. Deliberately
    -- tolerated-empty: hermes has no reasoning control and rejects any value.
    thinking_level TEXT NOT NULL DEFAULT '',
    -- Family-native config fragment (codex toml block, hermes overlay,
    -- reasonix user config, openclaw wrapper). SECRET-LIKE: string leaves
    -- under secret-looking keys are masked together with env.
    native_config JSONB NOT NULL DEFAULT '{}',
    -- Presence of 'private' matches runtime_profile. Creation forces
    -- 'workspace' until the read paths enforce creator visibility, for the
    -- same reason runtime_profile does: a "private" preset that every member
    -- can read is a lateral leak.
    visibility TEXT NOT NULL DEFAULT 'workspace' CHECK (visibility IN ('workspace', 'private')),
    created_by UUID,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

CREATE INDEX idx_provider_preset_workspace ON provider_preset(workspace_id);

-- Backs the picker: "which presets can I apply to THIS runtime".
CREATE INDEX idx_provider_preset_workspace_runtime_type
    ON provider_preset(workspace_id, runtime_type);

COMMENT ON COLUMN provider_preset.env IS
    'Environment injected into the agent process at launch. SECRET: masked on every read; a PATCH that echoes the mask keeps the persisted value.';

COMMENT ON COLUMN provider_preset.native_config IS
    'Family-native config fragment. String leaves under secret-looking keys are masked on read, same sentinel as env.';

-- The "sync to connected runtimes" action writes exactly this column: the
-- preset currently in force for every agent on that runtime. NULL = no preset,
-- agents run on their own configuration. Plain UUID with no DB FK: clearing it
-- when the preset is deleted is the application layer's responsibility.
ALTER TABLE agent_runtime
    ADD COLUMN active_provider_preset_id UUID;

COMMENT ON COLUMN agent_runtime.active_provider_preset_id IS
    'Provider preset currently in force for this runtime (NULL = none). Its env/model override each agent''s own values at task launch.';
