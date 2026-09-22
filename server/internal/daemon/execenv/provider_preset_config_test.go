package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Provider preset native config
//
// These pin the property the whole feature rests on — a preset's fragment lands
// in the file the TASK owns and wins over what was there — plus the two ways
// that can fail without taking a runtime down: a fragment that cannot be
// rendered, and one that collides with a key the config already defines.
// ---------------------------------------------------------------------------

func TestApplyCodexProviderPresetPrependsManagedBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := "model = \"gpt-5\"\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	err := applyCodexProviderPreset(path, map[string]any{
		"model":          "relay-model",
		"model_provider": "relay",
	}, nil)
	if err != nil {
		t.Fatalf("applyCodexProviderPreset: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, codexPresetBeginMarker) {
		t.Fatalf("managed block must come first so its bare keys precede every table header:\n%s", got)
	}

	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("generated config.toml is not valid TOML: %v\n%s", err, got)
	}
	if cfg["model_provider"] != "relay" {
		t.Fatalf("model_provider = %v, want the preset's value", cfg["model_provider"])
	}
	// The preset wins over the user's own value for the same key: the block
	// sits first, so it is its `model` that survives.
	if cfg["model"] != "relay-model" {
		t.Fatalf("model = %v, want the preset's value", cfg["model"])
	}
	profiles, ok := cfg["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("user profiles table lost: %v", cfg["profiles"])
	}
	if _, ok := profiles["fast"]; !ok {
		t.Fatalf("user profile dropped: %v", profiles)
	}
}

func TestApplyCodexProviderPresetRemovesStaleBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	stale := codexPresetBeginMarker + "\nmodel = \"old-relay\"\n" + codexPresetEndMarker + "\n\nmodel = \"gpt-5\"\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	// A runtime whose preset was cleared must not keep the old supplier's
	// config — that is the silent-stale-supplier bug this removal prevents.
	if err := applyCodexProviderPreset(path, nil, nil); err != nil {
		t.Fatalf("applyCodexProviderPreset: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), codexPresetBeginMarker) {
		t.Fatalf("stale managed block survived:\n%s", data)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("cleaned config is not valid TOML: %v\n%s", err, data)
	}
	if cfg["model"] != "gpt-5" {
		t.Fatalf("model = %v, want the user's own value back", cfg["model"])
	}
}

// A user config the daemon cannot parse must be left exactly as it is. The
// alternative — writing a preset block into a file we failed to understand —
// risks handing Codex a config that is broken in a new way, and a preset is
// never worth that.
func TestApplyCodexProviderPresetLeavesUnparseableConfigUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := "model = \"gpt-5\"\nthis is not toml\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyCodexProviderPreset(path, map[string]any{"model": "relay-model"}, nil); err != nil {
		t.Fatalf("an unparseable config must degrade, not fail: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != existing {
		t.Fatalf("config was rewritten:\n%s", data)
	}
}

// A preset that redefines a TABLE the user config already has must take the
// whole table — header AND body — or the body's keys are promoted to the root
// and a profile-only setting silently becomes a global one.
func TestApplyCodexProviderPresetReplacesWholeTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := "[profiles.fast]\nmodel = \"gpt-5-mini\"\nsandbox = \"danger-full-access\"\n\n[other]\nkeep = true\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyCodexProviderPreset(path, map[string]any{
		"profiles": map[string]any{"fast": map[string]any{"model": "relay-model"}},
	}, nil); err != nil {
		t.Fatalf("applyCodexProviderPreset: %v", err)
	}
	data, _ := os.ReadFile(path)
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("generated config.toml is not valid TOML: %v\n%s", err, data)
	}
	// The preset's `fast` profile replaces the user's whole table: the
	// sandbox setting the preset did not name is gone, not promoted.
	fast := cfg["profiles"].(map[string]any)["fast"].(map[string]any)
	if fast["model"] != "relay-model" {
		t.Fatalf("profiles.fast.model = %v, want the preset's value", fast["model"])
	}
	if _, ok := fast["sandbox"]; ok {
		t.Fatalf("stale table body survived: %v", fast)
	}
	// An unrelated table is untouched.
	if cfg["other"].(map[string]any)["keep"] != true {
		t.Fatalf("unrelated table dropped: %v", cfg["other"])
	}
}

func TestMergeProviderPresetFragment(t *testing.T) {
	t.Parallel()
	base := map[string]any{
		"model": "gpt-5",
		"providers": map[string]any{
			"openai": map[string]any{"base_url": "https://api.openai.com", "name": "openai"},
		},
		"flags": []any{"a"},
	}
	mergeProviderPresetFragment(base, map[string]any{
		"model": "relay-model",
		"providers": map[string]any{
			"openai": map[string]any{"base_url": "https://relay.example.com"},
		},
		"flags": []any{"b"},
	})

	if base["model"] != "relay-model" {
		t.Fatalf("scalar override lost: %v", base["model"])
	}
	openai := base["providers"].(map[string]any)["openai"].(map[string]any)
	if openai["base_url"] != "https://relay.example.com" {
		t.Fatalf("nested override lost: %v", openai["base_url"])
	}
	// A sibling the fragment did not name must survive.
	if openai["name"] != "openai" {
		t.Fatalf("nested sibling dropped: %v", openai["name"])
	}
	// Arrays are replaced, not merged: no family defines an array merge.
	if got := base["flags"].([]any)[0]; got != "b" {
		t.Fatalf("array should be replaced wholesale, got %v", got)
	}
}

func TestReasonixProjectConfigKeepsAskDeniedUnderPresetOverride(t *testing.T) {
	t.Parallel()
	// No user config: the owner's permissions are just the `ask` deny.
	body, err := reasonixProjectConfig("", map[string]any{
		"model":       "relay-reasoner",
		"permissions": map[string]any{"deny": []any{"dangerous"}},
	})
	if err != nil {
		t.Fatalf("reasonixProjectConfig: %v", err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("generated reasonix.toml is not valid TOML: %v\n%s", err, body)
	}
	if cfg["model"] != "relay-reasoner" {
		t.Fatalf("model = %v, want the preset's value", cfg["model"])
	}
	perms, ok := cfg["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions table missing: %v", cfg)
	}
	deny, _ := perms["deny"].([]any)
	has := func(want string) bool {
		for _, rule := range deny {
			if s, ok := rule.(string); ok && s == want {
				return true
			}
		}
		return false
	}
	if !has("dangerous") {
		t.Fatalf("preset's own deny rule lost: %v", deny)
	}
	// The invariant this file exists for must survive a preset that replaces
	// the whole [permissions] table.
	if !has(reasonixAskTool) {
		t.Fatalf("ask must stay denied even when the preset overrides permissions: %v", deny)
	}
}

func TestMergeYAMLFragmentIntoHermesConfig(t *testing.T) {
	t.Parallel()
	source := "model:\n  default: gpt-5\nskills:\n  external_dirs:\n    - /home/u/.hermes/skills\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		t.Fatal(err)
	}

	if err := mergeYAMLFragment(&doc, map[string]any{
		"model": map[string]any{"default": "relay-model"},
	}); err != nil {
		t.Fatalf("mergeYAMLFragment: %v", err)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	model := cfg["model"].(map[string]any)
	if model["default"] != "relay-model" {
		t.Fatalf("model.default = %v, want the preset's value", model["default"])
	}
	// A sibling branch the fragment did not name must survive.
	if _, ok := cfg["skills"]; !ok {
		t.Fatalf("unrelated top-level key dropped: %v", cfg)
	}
}
