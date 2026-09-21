package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Provider Presets
//
// A provider preset is a workspace-level bundle of "which supplier this
// runtime talks to": base URL, API key, model, thinking level and an optional
// family-native config fragment. Applying one to an agent_runtime sets
// `active_provider_preset_id`, and every agent on that runtime inherits it —
// one switch moves the whole machine between the official endpoint and a
// relay.
//
// Delivery is server-side: the preset rides along in the daemon's claim
// response (TaskAgentData) and is injected into the child process at launch.
// Nothing here ever writes the user's global CLI configuration — Multica gives
// every task its own workdir and env, so a global write would let concurrent
// tasks overwrite each other and would silently reconfigure the user's own
// machine.
//
// Precedence, by decision: agent configuration → preset override. A preset's
// env / model / thinking_level win over the agent's own custom_env / model.
//
// Secrets: preset env values are credentials in the common case. Every read
// path masks them, and a PATCH that echoes the mask keeps the persisted value
// instead of overwriting the real secret with three asterisks.
// ---------------------------------------------------------------------------

// providerPresetSecretMask is the sentinel a GET substitutes for any non-empty
// secret. Reusing the same sentinel for env values and for secret-looking
// leaves inside native_config keeps one round-trip rule for clients: send back
// what you were given and the value is preserved.
const providerPresetSecretMask = "***"

const (
	maxProviderPresetNameLen          = 100
	maxProviderPresetModelLen         = 200
	maxProviderPresetThinkingLevelLen = 64
	maxProviderPresetEnvKeys          = 64
	maxProviderPresetEnvValueLen      = 4096
	maxProviderPresetNativeConfigLen  = 64 * 1024
)

// NOTE: provider_preset.visibility is forced to 'workspace' for the same
// reason runtime_profile's is: the read paths do not yet enforce 'private', so
// accepting one would leak a "private" preset to every member. Re-expose a
// visibility control only once creator-visibility filtering exists.
const providerPresetDefaultVisibility = "workspace"

type ProviderPresetResponse struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	Name           string `json:"name"`
	RuntimeType    string `json:"runtime_type"`
	ProtocolFamily string `json:"protocol_family"`
	// Env is the preset's environment with every non-empty value replaced by
	// providerPresetSecretMask. Keys stay visible so the UI can show which
	// variables a preset sets; values never leave the server.
	Env map[string]string `json:"env"`
	// EnvKeyCount is len(env). Redundant today but keeps the count honest if a
	// future read path starts omitting entries.
	EnvKeyCount int `json:"env_key_count"`
	// NativeConfig is the family-native config fragment with secret-looking
	// string leaves masked.
	NativeConfig  any     `json:"native_config"`
	Model         string  `json:"model"`
	ThinkingLevel string  `json:"thinking_level"`
	Visibility    string  `json:"visibility"`
	CreatedBy     *string `json:"created_by"`
	Enabled       bool    `json:"enabled"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// presetSecretKeyHints mark a JSON string leaf as credential material. They are
// matched against a normalized key (lower-cased, separators collapsed to `_`),
// so `apiKey`, `API_KEY` and `api-key` all hit the same hint.
var presetSecretKeyHints = []string{"api_key", "token", "secret", "password", "authorization", "credential", "bearer"}

// isSecretLikeKey reports whether a config leaf's key names credential
// material. Deliberately conservative: a bare `key` is NOT a hint, because
// families use it for non-secret identifiers and over-masking a fragment the
// user must edit makes the editor useless.
func isSecretLikeKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return unicode.ToLower(r)
		case r == '-' || r == '.' || r == ' ':
			return '_'
		}
		return -1
	}, key)
	for _, hint := range presetSecretKeyHints {
		if strings.Contains(normalized, hint) {
			return true
		}
	}
	return false
}

// maskPresetSecrets replaces secret-looking string leaves in a decoded
// native_config tree, in place. No-op for any other shape so a family whose
// fragment is not an object passes through untouched.
func maskPresetSecrets(v any) {
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if s, ok := val.(string); ok && s != "" && isSecretLikeKey(k) {
				node[k] = providerPresetSecretMask
				continue
			}
			maskPresetSecrets(val)
		}
	case []any:
		for _, item := range node {
			maskPresetSecrets(item)
		}
	}
}

// restoreMaskedPresetSecrets walks an incoming native_config and the persisted
// one side by side, substituting the persisted value wherever the client
// echoed the mask. Without it the next PATCH after a GET would write three
// asterisks over the real secret. Missing or type-mismatched branches are left
// exactly as the client sent them.
func restoreMaskedPresetSecrets(incoming, persisted any) {
	inMap, ok := incoming.(map[string]any)
	if !ok {
		return
	}
	prevMap, ok := persisted.(map[string]any)
	if !ok {
		return
	}
	for k, val := range inMap {
		prev, exists := prevMap[k]
		if !exists {
			continue
		}
		if s, isStr := val.(string); isStr && s == providerPresetSecretMask {
			if ps, isPrevStr := prev.(string); isPrevStr && ps != "" {
				inMap[k] = ps
				continue
			}
		}
		restoreMaskedPresetSecrets(val, prev)
	}
}

// decodePresetEnv unmarshals the stored env JSONB. A corrupt or non-object
// payload yields an empty map rather than an error: a preset whose env cannot
// be read still has a name and a model the UI must be able to show and fix.
func decodePresetEnv(raw []byte) map[string]string {
	env := map[string]string{}
	if len(raw) == 0 {
		return env
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return map[string]string{}
	}
	if env == nil {
		return map[string]string{}
	}
	return env
}

// maskPresetEnv returns env with every non-empty value replaced by the mask.
func maskPresetEnv(env map[string]string) map[string]string {
	masked := make(map[string]string, len(env))
	for k, v := range env {
		if v != "" {
			masked[k] = providerPresetSecretMask
			continue
		}
		masked[k] = ""
	}
	return masked
}

// restoreMaskedPresetEnv puts the persisted value back wherever the client
// echoed the mask. A key the preset no longer has keeps the mask literally —
// there is no prior value to restore and the caller asked for that string.
func restoreMaskedPresetEnv(incoming, persisted map[string]string) {
	for k, v := range incoming {
		if v != providerPresetSecretMask {
			continue
		}
		if prev, ok := persisted[k]; ok && prev != "" {
			incoming[k] = prev
		}
	}
}

func providerPresetToResponse(p db.ProviderPreset) ProviderPresetResponse {
	env := decodePresetEnv(p.Env)
	var native any
	if len(p.NativeConfig) > 0 {
		_ = json.Unmarshal(p.NativeConfig, &native)
		maskPresetSecrets(native)
	}
	if native == nil {
		native = map[string]any{}
	}
	return ProviderPresetResponse{
		ID:             uuidToString(p.ID),
		WorkspaceID:    uuidToString(p.WorkspaceID),
		Name:           p.Name,
		RuntimeType:    p.RuntimeType,
		ProtocolFamily: p.ProtocolFamily,
		Env:            maskPresetEnv(env),
		EnvKeyCount:    len(env),
		NativeConfig:   native,
		Model:          p.Model,
		ThinkingLevel:  p.ThinkingLevel,
		Visibility:     p.Visibility,
		CreatedBy:      uuidToPtr(p.CreatedBy),
		Enabled:        p.Enabled,
		CreatedAt:      timestampToString(p.CreatedAt),
		UpdatedAt:      timestampToString(p.UpdatedAt),
	}
}

// resolvePresetRuntimeType validates a runtime type against the same registry
// the backends are built from and returns its protocol family.
func resolvePresetRuntimeType(runtimeType string) (string, string, bool) {
	runtimeType = strings.TrimSpace(runtimeType)
	if runtimeType == "" {
		return "", "", false
	}
	family, supported := agent.RuntimeProtocolFamily(runtimeType)
	if !supported {
		return "", "", false
	}
	return runtimeType, family, true
}

// validatePresetEnv checks env keys and values before they are persisted.
// Keys must be shell-safe identifiers: they become real environment variables
// in a child process, so a key containing `=` or whitespace would corrupt the
// environment block, and a NUL would truncate it.
func validatePresetEnv(env map[string]string) error {
	if len(env) > maxProviderPresetEnvKeys {
		return errors.New("env has too many keys")
	}
	for k, v := range env {
		if k == "" {
			return errors.New("env keys cannot be empty")
		}
		if strings.ContainsRune(k, '\x00') || strings.ContainsRune(v, '\x00') {
			return errors.New("env cannot contain NUL bytes")
		}
		if len(v) > maxProviderPresetEnvValueLen {
			return errors.New("env value is too long: " + k)
		}
		for i, r := range k {
			valid := r == '_' ||
				(r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(i > 0 && r >= '0' && r <= '9')
			if !valid {
				return errors.New("invalid env key: " + k)
			}
		}
	}
	return nil
}

// normalizePresetNativeConfig validates the family-native fragment and returns
// the bytes to store. It must be a JSON object: every family's fragment is a
// keyed config block, and accepting an array or scalar would put an
// uninterpretable value behind a field the UI renders as an editor.
func normalizePresetNativeConfig(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}"), nil
	}
	var node any
	if err := json.Unmarshal(trimmed, &node); err != nil {
		return nil, errors.New("native_config must be valid JSON")
	}
	if node == nil {
		return []byte("{}"), nil
	}
	if _, ok := node.(map[string]any); !ok {
		return nil, errors.New("native_config must be a JSON object")
	}
	if len(trimmed) > maxProviderPresetNativeConfigLen {
		return nil, errors.New("native_config is too large")
	}
	return trimmed, nil
}

type createProviderPresetRequest struct {
	Name          string            `json:"name"`
	RuntimeType   string            `json:"runtime_type"`
	Env           map[string]string `json:"env"`
	Model         string            `json:"model"`
	ThinkingLevel string            `json:"thinking_level"`
	NativeConfig  json.RawMessage   `json:"native_config"`
	Enabled       *bool             `json:"enabled"`
}

// CreateProviderPreset creates a workspace provider preset. Admin-gated by the
// router. runtime_type is validated against the agent backend whitelist and is
// immutable afterwards.
func (h *Handler) CreateProviderPreset(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}

	var req createProviderPresetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len([]rune(req.Name)) > maxProviderPresetNameLen {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	runtimeType, family, supported := resolvePresetRuntimeType(req.RuntimeType)
	if !supported {
		writeError(w, http.StatusBadRequest, "unsupported runtime_type: "+strings.TrimSpace(req.RuntimeType))
		return
	}
	if err := validatePresetEnv(req.Env); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.TrimSpace(req.Model)
	if len([]rune(model)) > maxProviderPresetModelLen {
		writeError(w, http.StatusBadRequest, "model is too long")
		return
	}
	thinkingLevel := strings.TrimSpace(req.ThinkingLevel)
	if len([]rune(thinkingLevel)) > maxProviderPresetThinkingLevelLen {
		writeError(w, http.StatusBadRequest, "thinking_level is too long")
		return
	}
	nativeConfig, err := normalizePresetNativeConfig(req.NativeConfig)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	envJSON, err := json.Marshal(envOrEmpty(req.Env))
	if err != nil {
		writeError(w, http.StatusBadRequest, "env must be a string map")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	preset, err := h.Queries.CreateProviderPreset(r.Context(), db.CreateProviderPresetParams{
		WorkspaceID:    wsUUID,
		Name:           req.Name,
		RuntimeType:    runtimeType,
		ProtocolFamily: family,
		Env:            envJSON,
		Model:          model,
		ThinkingLevel:  thinkingLevel,
		NativeConfig:   nativeConfig,
		Visibility:     providerPresetDefaultVisibility,
		CreatedBy:      member.UserID,
		Enabled:        enabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a provider preset with this name already exists")
			return
		}
		slog.Error("CreateProviderPreset failed", "error", err, "workspace_id", wsID)
		writeError(w, http.StatusInternalServerError, "failed to create provider preset")
		return
	}

	h.publish(protocol.EventWorkspaceUpdated, wsID, "member", uuidToString(member.UserID), map[string]any{
		"provider_preset_id": uuidToString(preset.ID),
	})
	writeJSON(w, http.StatusCreated, providerPresetToResponse(preset))
}

// ListProviderPresets returns the workspace's provider presets. Optional
// ?runtime_type narrows the list to the presets that can be applied to a
// runtime of that backend — the picker's query.
func (h *Handler) ListProviderPresets(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}

	var presets []db.ProviderPreset
	var err error
	if runtimeType := strings.TrimSpace(r.URL.Query().Get("runtime_type")); runtimeType != "" {
		presets, err = h.Queries.ListProviderPresetsByRuntimeType(r.Context(), db.ListProviderPresetsByRuntimeTypeParams{
			WorkspaceID: wsUUID,
			RuntimeType: runtimeType,
		})
	} else {
		presets, err = h.Queries.ListProviderPresets(r.Context(), wsUUID)
	}
	if err != nil {
		slog.Error("ListProviderPresets failed", "error", err, "workspace_id", wsID)
		writeError(w, http.StatusInternalServerError, "failed to list provider presets")
		return
	}
	resp := make([]ProviderPresetResponse, len(presets))
	for i, p := range presets {
		resp[i] = providerPresetToResponse(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider_presets": resp})
}

// GetProviderPreset returns one provider preset with its secrets masked.
func (h *Handler) GetProviderPreset(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	presetUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "presetId"), "preset id")
	if !ok {
		return
	}

	preset, err := h.Queries.GetProviderPresetForWorkspace(r.Context(), db.GetProviderPresetForWorkspaceParams{
		ID:          presetUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "provider preset not found")
		return
	}
	writeJSON(w, http.StatusOK, providerPresetToResponse(preset))
}

type updateProviderPresetRequest struct {
	Name          *string            `json:"name"`
	RuntimeType   *string            `json:"runtime_type"`
	Env           *map[string]string `json:"env"`
	Model         *string            `json:"model"`
	ThinkingLevel *string            `json:"thinking_level"`
	NativeConfig  json.RawMessage    `json:"native_config"`
	Enabled       *bool              `json:"enabled"`
}

// UpdateProviderPreset applies a partial update. runtime_type is immutable: a
// preset's env keys, native_config shape and model namespace are
// family-specific, so repointing an applied preset would feed one supplier's
// config to a different CLI. Masked secrets are restored from the persisted
// row before the write. Admin-gated by the router.
func (h *Handler) UpdateProviderPreset(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	presetUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "presetId"), "preset id")
	if !ok {
		return
	}

	var req updateProviderPresetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RuntimeType != nil {
		writeError(w, http.StatusBadRequest, "runtime_type is immutable; create a new preset")
		return
	}

	// Load the row first: restoring a masked secret needs the persisted value,
	// and the same read scopes the update to this workspace.
	current, err := h.Queries.GetProviderPresetForWorkspace(r.Context(), db.GetProviderPresetForWorkspaceParams{
		ID:          presetUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "provider preset not found")
		return
	}

	params := db.UpdateProviderPresetParams{ID: presetUUID, WorkspaceID: wsUUID}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		if len([]rune(name)) > maxProviderPresetNameLen {
			writeError(w, http.StatusBadRequest, "name is too long")
			return
		}
		params.Name = strToText(name)
	}
	if req.Model != nil {
		model := strings.TrimSpace(*req.Model)
		if len([]rune(model)) > maxProviderPresetModelLen {
			writeError(w, http.StatusBadRequest, "model is too long")
			return
		}
		params.Model = strToText(model)
	}
	if req.ThinkingLevel != nil {
		level := strings.TrimSpace(*req.ThinkingLevel)
		if len([]rune(level)) > maxProviderPresetThinkingLevelLen {
			writeError(w, http.StatusBadRequest, "thinking_level is too long")
			return
		}
		params.ThinkingLevel = strToText(level)
	}
	if req.Env != nil {
		env := *req.Env
		if env == nil {
			env = map[string]string{}
		}
		restoreMaskedPresetEnv(env, decodePresetEnv(current.Env))
		if err := validatePresetEnv(env); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, err := json.Marshal(env)
		if err != nil {
			writeError(w, http.StatusBadRequest, "env must be a string map")
			return
		}
		params.Env = encoded
	}
	if req.NativeConfig != nil {
		normalized, err := normalizePresetNativeConfig(req.NativeConfig)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var incoming any
		_ = json.Unmarshal(normalized, &incoming)
		var persisted any
		if len(current.NativeConfig) > 0 {
			_ = json.Unmarshal(current.NativeConfig, &persisted)
		}
		restoreMaskedPresetSecrets(incoming, persisted)
		encoded, err := json.Marshal(incoming)
		if err != nil {
			writeError(w, http.StatusBadRequest, "native_config must be a JSON object")
			return
		}
		params.NativeConfig = encoded
	}
	if req.Enabled != nil {
		params.Enabled = pgtype.Bool{Bool: *req.Enabled, Valid: true}
	}

	preset, err := h.Queries.UpdateProviderPreset(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "provider preset not found")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a provider preset with this name already exists")
			return
		}
		slog.Error("UpdateProviderPreset failed", "error", err, "preset_id", uuidToString(presetUUID))
		writeError(w, http.StatusInternalServerError, "failed to update provider preset")
		return
	}

	h.publish(protocol.EventWorkspaceUpdated, wsID, "member", uuidToString(member.UserID), map[string]any{
		"provider_preset_id": uuidToString(preset.ID),
	})
	writeJSON(w, http.StatusOK, providerPresetToResponse(preset))
}

// DeleteProviderPreset removes a preset and unbinds it from every runtime that
// was using it. Migration 502 added no DB FK, so this app-layer cleanup is what
// keeps agent_runtime.active_provider_preset_id from dangling: a stale pointer
// would make every later claim try to resolve a row that no longer exists.
// Unbinding is deliberate rather than refusing — a preset in force is a
// configuration choice, not data, so dropping the preset falls back to each
// agent's own configuration instead of bricking the runtime.
func (h *Handler) DeleteProviderPreset(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	presetUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "presetId"), "preset id")
	if !ok {
		return
	}

	if _, err := h.Queries.GetProviderPresetForWorkspace(r.Context(), db.GetProviderPresetForWorkspaceParams{
		ID:          presetUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "provider preset not found")
		return
	}

	cleared, err := h.Queries.ClearProviderPresetFromRuntimes(r.Context(), db.ClearProviderPresetFromRuntimesParams{
		WorkspaceID: wsUUID,
		PresetID:    presetUUID,
	})
	if err != nil {
		slog.Error("ClearProviderPresetFromRuntimes failed", "error", err, "preset_id", uuidToString(presetUUID))
		writeError(w, http.StatusInternalServerError, "failed to unbind provider preset")
		return
	}
	if err := h.Queries.DeleteProviderPreset(r.Context(), db.DeleteProviderPresetParams{
		ID:          presetUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		slog.Error("DeleteProviderPreset failed", "error", err, "preset_id", uuidToString(presetUUID))
		writeError(w, http.StatusInternalServerError, "failed to delete provider preset")
		return
	}

	// Runtimes changed (their active preset is gone), so clients refetch them;
	// the workspace-level event carries the deletion itself.
	for _, runtimeID := range cleared {
		h.publish(protocol.EventDaemonRegister, wsID, "member", uuidToString(member.UserID), map[string]any{
			"runtime_id": uuidToString(runtimeID),
		})
	}
	h.publish(protocol.EventWorkspaceUpdated, wsID, "member", uuidToString(member.UserID), map[string]any{
		"deleted_provider_preset_id": uuidToString(presetUUID),
	})

	w.WriteHeader(http.StatusNoContent)
}

// envOrEmpty normalizes a nil env map so encoding/json emits `{}` rather than
// `null` — the column is NOT NULL DEFAULT '{}' and every reader unmarshals it
// into a map.
func envOrEmpty(env map[string]string) map[string]string {
	if env == nil {
		return map[string]string{}
	}
	return env
}

// presetAppliesToRuntime reports whether a preset authored for one backend can
// be applied to a runtime of another. Exact match always wins; otherwise the
// two must resolve to the same protocol family, which is what lets a built-in
// runtime identity (e.g. "omp") accept a preset written for its family.
func presetAppliesToRuntime(presetRuntimeType, runtimeProvider string) bool {
	if presetRuntimeType == runtimeProvider {
		return true
	}
	presetFamily, presetOK := agent.RuntimeProtocolFamily(presetRuntimeType)
	runtimeFamily, runtimeOK := agent.RuntimeProtocolFamily(runtimeProvider)
	return presetOK && runtimeOK && presetFamily == runtimeFamily
}

// presetOverride is the minimal shape the override disclosure needs from a
// preset: name plus the three fields that can displace an agent's own values.
// Kept separate from db.ProviderPreset so the batch query — which selects only
// these columns — and the single-runtime query share one computation.
type presetOverride struct {
	ID            string
	Name          string
	Env           []byte
	Model         string
	ThinkingLevel string
}

// runtimePresetOverrideFor computes the disclosure from an agent row and the
// preset its runtime has in force. It is pure: no query, no error path. Both
// the detail response and the list response go through it so the two can never
// disagree about what "overridden" means.
func runtimePresetOverrideFor(agentRow db.Agent, runtimeID string, preset presetOverride) *RuntimePresetOverride {
	var customEnv map[string]string
	if len(agentRow.CustomEnv) > 0 {
		_ = json.Unmarshal(agentRow.CustomEnv, &customEnv)
	}
	presetEnv := decodePresetEnv(preset.Env)

	// Counted, never named: these are the agent's own secret names, and the
	// disclosure is readable by anyone who can read the agent.
	overridden := 0
	for key := range customEnv {
		if _, ok := presetEnv[key]; ok {
			overridden++
		}
	}

	return &RuntimePresetOverride{
		PresetID:   preset.ID,
		PresetName: preset.Name,
		RuntimeID:  runtimeID,
		// An empty preset field means "inherit", so it overrides nothing and
		// must not be counted. The agent must also have set one: replacing
		// "unset" with the preset's value is the preset doing its job, not
		// overriding the agent.
		OverriddenEnvKeyCount:   overridden,
		ModelOverridden:         preset.Model != "" && agentRow.Model.String != "",
		ThinkingLevelOverridden: preset.ThinkingLevel != "" && agentRow.ThinkingLevel.String != "",
	}
}

// attachRuntimePresetOverride fills in the disclosure that a runtime-level
// preset is overriding part of this agent's own configuration.
//
// The agent's custom_env / model are still editable and still stored — a
// preset does not rewrite them — but at launch the preset wins, so without
// this the agent's settings page would show configuration that is not in
// force. Nothing here fails the request: a read that errors leaves the
// summary off rather than turning an agent detail page into a 500.
func (h *Handler) attachRuntimePresetOverride(ctx context.Context, resp *AgentResponse, agentRow db.Agent) {
	if !agentRow.RuntimeID.Valid {
		return
	}
	preset, err := h.Queries.GetActiveProviderPresetForRuntime(ctx, agentRow.RuntimeID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Debug("provider preset override: no active preset for runtime",
				"runtime_id", uuidToString(agentRow.RuntimeID), "error", err)
		}
		return
	}
	resp.RuntimePresetOverride = runtimePresetOverrideFor(agentRow, uuidToString(agentRow.RuntimeID), presetOverride{
		ID:            uuidToString(preset.ID),
		Name:          preset.Name,
		Env:           preset.Env,
		Model:         preset.Model,
		ThinkingLevel: preset.ThinkingLevel,
	})
}

// attachRuntimePresetOverrides is the list-shaped sibling: one batch read for
// every runtime the response touches, then the same per-agent computation.
// Doing this per row would be the classic N+1 on the busiest read in the app.
func (h *Handler) attachRuntimePresetOverrides(ctx context.Context, resps []AgentResponse, agentRows []db.Agent) {
	runtimeIDs := make([]pgtype.UUID, 0, len(agentRows))
	seen := make(map[string]struct{}, len(agentRows))
	for _, a := range agentRows {
		if !a.RuntimeID.Valid {
			continue
		}
		id := uuidToString(a.RuntimeID)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		runtimeIDs = append(runtimeIDs, a.RuntimeID)
	}
	if len(runtimeIDs) == 0 {
		return
	}
	presets, err := h.Queries.ListActiveProviderPresetsForRuntimes(ctx, runtimeIDs)
	if err != nil {
		slog.Debug("provider preset override: batch read failed; list renders without the disclosure", "error", err)
		return
	}
	byRuntime := make(map[string]presetOverride, len(presets))
	for _, p := range presets {
		byRuntime[uuidToString(p.RuntimeID)] = presetOverride{
			ID:            uuidToString(p.ID),
			Name:          p.Name,
			Env:           p.Env,
			Model:         p.Model,
			ThinkingLevel: p.ThinkingLevel,
		}
	}
	for i := range resps {
		if i >= len(agentRows) {
			break
		}
		a := agentRows[i]
		if !a.RuntimeID.Valid {
			continue
		}
		runtimeID := uuidToString(a.RuntimeID)
		preset, ok := byRuntime[runtimeID]
		if !ok {
			continue
		}
		resps[i].RuntimePresetOverride = runtimePresetOverrideFor(a, runtimeID, preset)
	}
}
