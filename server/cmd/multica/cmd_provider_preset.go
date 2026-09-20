package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// ---------------------------------------------------------------------------
// `multica provider-preset ...` — runtime-level supplier configuration
//
// A provider preset is a workspace-level bundle of "which supplier this
// runtime talks to": base URL, API key, model, thinking level and an optional
// family-native config fragment. `apply` points a runtime at one, and every
// agent on that runtime inherits it — one command moves a whole machine
// between the official endpoint and a relay.
//
// The preset lives server-side and is delivered to the daemon with the task
// claim, so it works on any machine the runtime is registered from. Nothing
// here touches the user's global CLI configuration.
//
// Secret handling matches `multica agent`: env travels through three mutually
// exclusive channels (--env / --env-stdin / --env-file) so a real API key can
// be kept out of shell history and 'ps'. Reads come back masked — a value of
// *** is what the server stored, and echoing it back in an update keeps the
// original.
// ---------------------------------------------------------------------------

var providerPresetCmd = &cobra.Command{
	Use:   "provider-preset",
	Short: "Manage workspace provider presets (base URL / API key / model per runtime)",
}

var providerPresetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List provider presets in the workspace",
	RunE:  runProviderPresetList,
}

var providerPresetCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a provider preset",
	RunE:  runProviderPresetCreate,
}

var providerPresetUpdateCmd = &cobra.Command{
	Use:   "update <preset-id>",
	Short: "Update a provider preset (runtime_type is immutable)",
	Args:  exactArgs(1),
	RunE:  runProviderPresetUpdate,
}

var providerPresetDeleteCmd = &cobra.Command{
	Use:   "delete <preset-id>",
	Short: "Delete a provider preset and unbind it from every runtime using it",
	Args:  exactArgs(1),
	RunE:  runProviderPresetDelete,
}

var providerPresetApplyCmd = &cobra.Command{
	Use:   "apply <preset-id>",
	Short: "Point a runtime at this preset (every agent on it inherits the preset)",
	Args:  exactArgs(1),
	RunE:  runProviderPresetApply,
}

var providerPresetUnapplyCmd = &cobra.Command{
	Use:   "unapply",
	Short: "Clear a runtime's preset so its agents use their own configuration",
	RunE:  runProviderPresetUnapply,
}

func init() {
	providerPresetCmd.AddCommand(providerPresetListCmd)
	providerPresetCmd.AddCommand(providerPresetCreateCmd)
	providerPresetCmd.AddCommand(providerPresetUpdateCmd)
	providerPresetCmd.AddCommand(providerPresetDeleteCmd)
	providerPresetCmd.AddCommand(providerPresetApplyCmd)
	providerPresetCmd.AddCommand(providerPresetUnapplyCmd)

	// list
	providerPresetListCmd.Flags().String("runtime-type", "", "Only show presets that can be applied to this runtime type")
	providerPresetListCmd.Flags().String("output", "table", "Output format: table or json")

	// create
	providerPresetCreateCmd.Flags().String("name", "", "Human-readable preset name (required)")
	providerPresetCreateCmd.Flags().String("runtime-type", "", "Backend this preset configures, e.g. claude or codex (required)")
	registerProviderPresetEnvFlags(providerPresetCreateCmd)
	providerPresetCreateCmd.Flags().String("model", "", "Runtime-native model id (empty = inherit)")
	providerPresetCreateCmd.Flags().String("thinking-level", "", "Runtime-native reasoning/effort token (empty = inherit)")
	providerPresetCreateCmd.Flags().String("native-config", "", "Family-native config fragment as a JSON object")
	providerPresetCreateCmd.Flags().String("native-config-file", "", "Read the family-native config fragment from a file")
	providerPresetCreateCmd.Flags().Bool("enabled", true, "Enable or disable the preset")
	providerPresetCreateCmd.Flags().String("output", "json", "Output format: table or json")

	// update
	providerPresetUpdateCmd.Flags().String("name", "", "New name")
	registerProviderPresetEnvFlags(providerPresetUpdateCmd)
	providerPresetUpdateCmd.Flags().String("model", "", "New model (empty string clears it)")
	providerPresetUpdateCmd.Flags().String("thinking-level", "", "New thinking level (empty string clears it)")
	providerPresetUpdateCmd.Flags().String("native-config", "", "New family-native config fragment as a JSON object")
	providerPresetUpdateCmd.Flags().String("native-config-file", "", "Read the new family-native config fragment from a file")
	providerPresetUpdateCmd.Flags().Bool("enabled", true, "Enable or disable the preset")
	providerPresetUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// apply / unapply
	providerPresetApplyCmd.Flags().String("runtime", "", "Runtime id to apply the preset to (required)")
	providerPresetUnapplyCmd.Flags().String("runtime", "", "Runtime id to clear the preset from (required)")
}

// registerProviderPresetEnvFlags adds the three secret channels under the
// names resolveCustomEnv reads, so presets reuse the agent command's secret
// plumbing verbatim instead of growing a parallel implementation.
func registerProviderPresetEnvFlags(cmd *cobra.Command) {
	cmd.Flags().String("custom-env", "", "Preset env as a JSON object, e.g. '{\"ANTHROPIC_BASE_URL\":\"...\",\"ANTHROPIC_API_KEY\":\"...\"}'. Treated as secret material — prefer --custom-env-stdin or --custom-env-file for real keys. Pass '{}' to set an empty map.")
	cmd.Flags().Bool("custom-env-stdin", false, "Read the preset env JSON object from stdin. Keeps secrets out of shell history and 'ps'. Mutually exclusive with --custom-env and --custom-env-file.")
	cmd.Flags().String("custom-env-file", "", "Read the preset env JSON object from a file path (suggested mode: 0600). Mutually exclusive with --custom-env and --custom-env-stdin.")
}

// providerPresetsPath builds the workspace-scoped collection path.
func providerPresetsPath(workspaceID string) string {
	return fmt.Sprintf("/api/workspaces/%s/provider-presets", workspaceID)
}

func runProviderPresetList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	path := providerPresetsPath(workspaceID)
	if runtimeType, _ := cmd.Flags().GetString("runtime-type"); strings.TrimSpace(runtimeType) != "" {
		path += "?runtime_type=" + url.QueryEscape(strings.TrimSpace(runtimeType))
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		ProviderPresets []map[string]any `json:"provider_presets"`
	}
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return fmt.Errorf("list provider presets: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp.ProviderPresets)
	}
	printProviderPresetTable(resp.ProviderPresets)
	return nil
}

func runProviderPresetCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	runtimeType, _ := cmd.Flags().GetString("runtime-type")
	runtimeType = strings.TrimSpace(runtimeType)
	if runtimeType == "" {
		return fmt.Errorf("--runtime-type is required")
	}
	if _, ok := agent.RuntimeProtocolFamily(runtimeType); !ok {
		return fmt.Errorf("unsupported --runtime-type %q; must be one of %s",
			runtimeType, strings.Join(agent.SupportedTypes, ", "))
	}

	body := map[string]any{
		"name":         name,
		"runtime_type": runtimeType,
	}
	env, hasEnv, err := resolveCustomEnv(cmd)
	if err != nil {
		return err
	}
	if hasEnv {
		body["env"] = env
	}
	if cmd.Flags().Changed("model") {
		v, _ := cmd.Flags().GetString("model")
		body["model"] = v
	}
	if cmd.Flags().Changed("thinking-level") {
		v, _ := cmd.Flags().GetString("thinking-level")
		body["thinking_level"] = v
	}
	nativeConfig, hasNativeConfig, err := resolveProviderPresetNativeConfig(cmd)
	if err != nil {
		return err
	}
	if hasNativeConfig {
		body["native_config"] = nativeConfig
	}
	if cmd.Flags().Changed("enabled") {
		v, _ := cmd.Flags().GetBool("enabled")
		body["enabled"] = v
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var preset map[string]any
	if err := client.PostJSON(ctx, providerPresetsPath(workspaceID), body, &preset); err != nil {
		return fmt.Errorf("create provider preset: %w", err)
	}
	return outputProviderPreset(cmd, preset)
}

func runProviderPresetUpdate(cmd *cobra.Command, args []string) error {
	presetID := args[0]

	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	env, hasEnv, err := resolveCustomEnv(cmd)
	if err != nil {
		return err
	}
	if hasEnv {
		body["env"] = env
	}
	if cmd.Flags().Changed("model") {
		v, _ := cmd.Flags().GetString("model")
		body["model"] = v
	}
	if cmd.Flags().Changed("thinking-level") {
		v, _ := cmd.Flags().GetString("thinking-level")
		body["thinking_level"] = v
	}
	nativeConfig, hasNativeConfig, err := resolveProviderPresetNativeConfig(cmd)
	if err != nil {
		return err
	}
	if hasNativeConfig {
		body["native_config"] = nativeConfig
	}
	if cmd.Flags().Changed("enabled") {
		v, _ := cmd.Flags().GetBool("enabled")
		body["enabled"] = v
	}
	if len(body) == 0 {
		return fmt.Errorf("no fields to update: pass at least one of --name, --custom-env*, --model, --thinking-level, --native-config*, --enabled")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var preset map[string]any
	if err := client.PatchJSON(ctx, providerPresetsPath(workspaceID)+"/"+presetID, body, &preset); err != nil {
		return fmt.Errorf("update provider preset: %w", err)
	}
	return outputProviderPreset(cmd, preset)
}

func runProviderPresetDelete(cmd *cobra.Command, args []string) error {
	presetID := args[0]

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, providerPresetsPath(workspaceID)+"/"+presetID); err != nil {
		return fmt.Errorf("delete provider preset: %w", err)
	}
	fmt.Printf("Deleted provider preset %s\n", presetID)
	fmt.Println("Runtimes that were using it now run on each agent's own configuration.")
	return nil
}

func runProviderPresetApply(cmd *cobra.Command, args []string) error {
	presetID := args[0]
	runtimeID, _ := cmd.Flags().GetString("runtime")
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return fmt.Errorf("--runtime is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var runtime map[string]any
	body := map[string]any{"active_provider_preset_id": presetID}
	if err := client.PatchJSON(ctx, "/api/runtimes/"+runtimeID, body, &runtime); err != nil {
		return fmt.Errorf("apply provider preset to runtime %s: %w", runtimeID, err)
	}
	fmt.Printf("Applied provider preset %s to runtime %s.\n", presetID, runtimeID)
	fmt.Println("Agents on that runtime pick it up on their next task; no restart needed.")
	return nil
}

func runProviderPresetUnapply(cmd *cobra.Command, _ []string) error {
	runtimeID, _ := cmd.Flags().GetString("runtime")
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return fmt.Errorf("--runtime is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	// null, not "": the server distinguishes "clear" from "not sent" by the
	// field's presence, and an empty string is not a valid preset id.
	var runtime map[string]any
	body := map[string]any{"active_provider_preset_id": nil}
	if err := client.PatchJSON(ctx, "/api/runtimes/"+runtimeID, body, &runtime); err != nil {
		return fmt.Errorf("clear provider preset on runtime %s: %w", runtimeID, err)
	}
	fmt.Printf("Cleared the provider preset on runtime %s.\n", runtimeID)
	return nil
}

// resolveProviderPresetNativeConfig collects --native-config / --native-config-file.
// The two are mutually exclusive for the same reason the env channels are: a
// secret fed twice is either a mistake or a leak.
func resolveProviderPresetNativeConfig(cmd *cobra.Command) (json.RawMessage, bool, error) {
	inline := cmd.Flags().Changed("native-config")
	fromFile := cmd.Flags().Changed("native-config-file")
	if inline && fromFile {
		return nil, false, fmt.Errorf("--native-config and --native-config-file are mutually exclusive; pick one")
	}
	if !inline && !fromFile {
		return nil, false, nil
	}
	raw := ""
	if inline {
		v, _ := cmd.Flags().GetString("native-config")
		raw = v
	} else {
		v, _ := cmd.Flags().GetString("native-config-file")
		if strings.TrimSpace(v) == "" {
			return nil, false, fmt.Errorf("--native-config-file: path must not be empty")
		}
		buf, err := os.ReadFile(v)
		if err != nil {
			// Filesystem errors may include the path but not the contents.
			return nil, false, fmt.Errorf("read --native-config-file: %w", err)
		}
		raw = string(buf)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, false, fmt.Errorf("native config is empty; pass '{}' to clear")
	}
	// Validate locally so a typo fails before the server rounds out the
	// native-config editor's own error message.
	var node any
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		return nil, false, fmt.Errorf("native config must be a valid JSON object")
	}
	if _, ok := node.(map[string]any); !ok {
		return nil, false, fmt.Errorf("native config must be a JSON object")
	}
	return json.RawMessage(raw), true, nil
}

// outputProviderPreset renders a single preset honoring --output.
func outputProviderPreset(cmd *cobra.Command, preset map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, preset)
	}
	printProviderPresetTable([]map[string]any{preset})
	return nil
}

// printProviderPresetTable renders presets as a stable, sorted table.
//
// Values are not printed at all — only the key count. The masked env the
// server returns is `{"KEY": "***"}` for every set key, so a value column
// would be three asterisks repeated, and printing keys would still advertise
// which credentials a workspace holds.
func printProviderPresetTable(presets []map[string]any) {
	headers := []string{"ID", "NAME", "RUNTIME_TYPE", "MODEL", "ENV_KEYS", "ENABLED"}
	rows := make([][]string, 0, len(presets))
	for _, p := range presets {
		rows = append(rows, []string{
			strVal(p, "id"),
			strVal(p, "name"),
			strVal(p, "runtime_type"),
			strVal(p, "model"),
			strVal(p, "env_key_count"),
			strVal(p, "enabled"),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][1] < rows[j][1] })
	cli.PrintTable(os.Stdout, headers, rows)
}
