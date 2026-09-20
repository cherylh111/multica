package handler

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// Provider preset secrets
//
// These pin the two halves of the round trip the API depends on: a GET must
// never hand a real credential to a client, and a PATCH that echoes the mask
// must restore the persisted value instead of writing three asterisks over it.
// Either half failing alone is silent data loss — the secret is gone the next
// time a task starts.
// ---------------------------------------------------------------------------

func TestMaskPresetEnvMasksEveryNonEmptyValue(t *testing.T) {
	got := maskPresetEnv(map[string]string{
		"ANTHROPIC_BASE_URL": "https://relay.example.com",
		"ANTHROPIC_API_KEY":  "sk-ant-real-secret",
		"EMPTY":              "",
	})
	if got["ANTHROPIC_API_KEY"] != providerPresetSecretMask {
		t.Fatalf("api key not masked: %q", got["ANTHROPIC_API_KEY"])
	}
	if got["ANTHROPIC_BASE_URL"] != providerPresetSecretMask {
		t.Fatalf("base url not masked: %q", got["ANTHROPIC_BASE_URL"])
	}
	// An empty value carries no secret; masking it would invent one.
	if got["EMPTY"] != "" {
		t.Fatalf("empty value should stay empty, got %q", got["EMPTY"])
	}
}

func TestRestoreMaskedPresetEnvRoundTrip(t *testing.T) {
	persisted := map[string]string{
		"ANTHROPIC_API_KEY":  "sk-ant-real-secret",
		"ANTHROPIC_BASE_URL": "https://relay.example.com",
	}
	// Exactly what a GET returned for this preset.
	incoming := map[string]string{
		"ANTHROPIC_API_KEY":  providerPresetSecretMask,
		"ANTHROPIC_BASE_URL": "https://new-relay.example.com",
	}
	restoreMaskedPresetEnv(incoming, persisted)
	if incoming["ANTHROPIC_API_KEY"] != "sk-ant-real-secret" {
		t.Fatalf("masked value not restored: %q", incoming["ANTHROPIC_API_KEY"])
	}
	// A real edit must survive — restoring is not "ignore the request".
	if incoming["ANTHROPIC_BASE_URL"] != "https://new-relay.example.com" {
		t.Fatalf("real edit overwritten: %q", incoming["ANTHROPIC_BASE_URL"])
	}
}

func TestRestoreMaskedPresetEnvLeavesUnknownMaskAlone(t *testing.T) {
	// A key the preset has never held has nothing to restore, so the literal
	// mask the client sent is what gets stored. Surprising, but the
	// alternative — dropping the key — silently ignores an explicit write.
	incoming := map[string]string{"BRAND_NEW": providerPresetSecretMask}
	restoreMaskedPresetEnv(incoming, map[string]string{})
	if incoming["BRAND_NEW"] != providerPresetSecretMask {
		t.Fatalf("expected literal mask to survive, got %q", incoming["BRAND_NEW"])
	}
}

func TestIsSecretLikeKey(t *testing.T) {
	secretKeys := []string{
		"apiKey", "API_KEY", "api-key",
		"token", "access_token", "authToken",
		"secret", "client_secret",
		"password", "authorization", "credentials", "bearer",
	}
	for _, k := range secretKeys {
		if !isSecretLikeKey(k) {
			t.Errorf("expected %q to be treated as secret", k)
		}
	}
	plainKeys := []string{
		"model", "model_provider", "base_url", "workspace", "key", "name",
	}
	for _, k := range plainKeys {
		if isSecretLikeKey(k) {
			t.Errorf("expected %q NOT to be treated as secret", k)
		}
	}
}

func TestMaskPresetSecretsWalksNestedNativeConfig(t *testing.T) {
	in := map[string]any{
		"model": "gpt-5",
		"providers": map[string]any{
			"relay": map[string]any{
				"base_url": "https://relay.example.com",
				"api_key":  "sk-real",
			},
		},
		"extra": []any{map[string]any{"token": "t-real"}},
	}
	maskPresetSecrets(in)

	providers := in["providers"].(map[string]any)
	relay := providers["relay"].(map[string]any)
	if relay["api_key"] != providerPresetSecretMask {
		t.Fatalf("nested api_key not masked: %v", relay["api_key"])
	}
	// Non-secret leaves must stay readable — this is a config fragment the
	// user edits in place.
	if relay["base_url"] != "https://relay.example.com" {
		t.Fatalf("non-secret leaf masked: %v", relay["base_url"])
	}
	if in["model"] != "gpt-5" {
		t.Fatalf("non-secret top-level leaf masked: %v", in["model"])
	}
	extra := in["extra"].([]any)[0].(map[string]any)
	if extra["token"] != providerPresetSecretMask {
		t.Fatalf("array-nested token not masked: %v", extra["token"])
	}
}

func TestRestoreMaskedPresetSecretsRoundTrip(t *testing.T) {
	persistedRaw := `{"providers":{"relay":{"base_url":"https://old.example.com","api_key":"sk-real"}}}`
	// What the UI sends back after a GET: base_url edited, api_key untouched.
	incomingRaw := `{"providers":{"relay":{"base_url":"https://new.example.com","api_key":"***"}}}`

	var persisted, incoming any
	if err := json.Unmarshal([]byte(persistedRaw), &persisted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(incomingRaw), &incoming); err != nil {
		t.Fatal(err)
	}
	restoreMaskedPresetSecrets(incoming, persisted)

	relay := incoming.(map[string]any)["providers"].(map[string]any)["relay"].(map[string]any)
	if relay["api_key"] != "sk-real" {
		t.Fatalf("masked secret not restored: %v", relay["api_key"])
	}
	if relay["base_url"] != "https://new.example.com" {
		t.Fatalf("real edit lost: %v", relay["base_url"])
	}
}

func TestValidatePresetEnv(t *testing.T) {
	valid := map[string]string{
		"ANTHROPIC_BASE_URL": "https://relay.example.com",
		"lower_case":         "ok",
		"A1":                 "ok",
	}
	if err := validatePresetEnv(valid); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}

	// These all become real environment variables in a child process, so a bad
	// key would corrupt the environment block rather than fail loudly.
	for name, env := range map[string]map[string]string{
		"empty key":     {"": "v"},
		"equals in key": {"A=B": "v"},
		"space in key":  {"A B": "v"},
		"leading digit": {"1A": "v"},
		"NUL in key":    {"A\x00": "v"},
		"NUL in value":  {"A": "v\x00"},
	} {
		if err := validatePresetEnv(env); err == nil {
			t.Errorf("expected %s to be rejected", name)
		}
	}

	tooMany := map[string]string{}
	for i := 0; i <= maxProviderPresetEnvKeys; i++ {
		tooMany[string(rune('A'+i%26))+string(rune('a'+i/26))] = "v"
	}
	if err := validatePresetEnv(tooMany); err == nil {
		t.Error("expected an over-sized env to be rejected")
	}
}

func TestNormalizePresetNativeConfig(t *testing.T) {
	got, err := normalizePresetNativeConfig(nil)
	if err != nil {
		t.Fatalf("nil native_config should default to {}: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("expected {}, got %s", got)
	}

	if _, err := normalizePresetNativeConfig(json.RawMessage(`[]`)); err == nil {
		t.Error("expected an array to be rejected: every family's fragment is a keyed block")
	}
	if _, err := normalizePresetNativeConfig(json.RawMessage(`"nope"`)); err == nil {
		t.Error("expected a scalar to be rejected")
	}
	if _, err := normalizePresetNativeConfig(json.RawMessage(`{`)); err == nil {
		t.Error("expected invalid JSON to be rejected")
	}

	got, err = normalizePresetNativeConfig(json.RawMessage(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatalf("object should be accepted: %v", err)
	}
	if string(got) != `{"model":"gpt-5"}` {
		t.Fatalf("unexpected normalization: %s", got)
	}
}

func TestPresetAppliesToRuntime(t *testing.T) {
	if !presetAppliesToRuntime("claude", "claude") {
		t.Error("exact match must apply")
	}
	if presetAppliesToRuntime("claude", "codex") {
		t.Error("a claude preset must not apply to a codex runtime")
	}
	// Reasonix and hermes are distinct families with distinct config shapes.
	if presetAppliesToRuntime("hermes", "reasonix") {
		t.Error("a hermes preset must not apply to a reasonix runtime")
	}
}

func TestDecodePresetEnvToleratesCorruptPayload(t *testing.T) {
	// A preset whose env cannot be read must still render: the UI needs its
	// name and model to let the owner fix it.
	if got := decodePresetEnv([]byte(`not json`)); len(got) != 0 {
		t.Fatalf("expected empty map for corrupt payload, got %v", got)
	}
	if got := decodePresetEnv(nil); len(got) != 0 {
		t.Fatalf("expected empty map for nil payload, got %v", got)
	}
	got := decodePresetEnv([]byte(`{"A":"b"}`))
	if got["A"] != "b" {
		t.Fatalf("unexpected decode: %v", got)
	}
}

func TestResolvePresetRuntimeType(t *testing.T) {
	if _, _, ok := resolvePresetRuntimeType(""); ok {
		t.Error("empty runtime_type must be rejected")
	}
	if _, _, ok := resolvePresetRuntimeType("not-a-backend"); ok {
		t.Error("unknown runtime_type must be rejected")
	}
	rt, family, ok := resolvePresetRuntimeType("  claude  ")
	if !ok {
		t.Fatal("claude must resolve")
	}
	if rt != "claude" || family != "claude" {
		t.Fatalf("unexpected resolution: %q / %q", rt, family)
	}
}
