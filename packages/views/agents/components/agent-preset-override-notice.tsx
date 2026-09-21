"use client";

import { Info } from "lucide-react";
import type { AgentRuntimePresetOverride } from "@multica/core/types";
import { useT } from "../../i18n";

/**
 * Discloses that a runtime-level provider preset is overriding parts of this
 * agent's own configuration.
 *
 * Without it the agent's settings page is a lie: the env keys and the model
 * are still stored and still editable, but at launch the runtime's preset
 * wins, so what the page shows is not what runs. The override is computed
 * server-side and shipped on the agent response — the client cannot derive
 * it, because the agent's env keys never reach the browser (MUL-2600) and
 * the preset's values never leave the server unmasked.
 *
 * `scope` keeps each surface honest about what it is talking about: the env
 * tab leads with the env count, the model row leads with the model. Neither
 * suppresses the other's line in the "all" scope, because a user who changed
 * three things needs to know all three lost.
 */
export function AgentPresetOverrideNotice({
  override,
  scope = "all",
  className = "",
}: {
  override?: AgentRuntimePresetOverride;
  scope?: "all" | "env" | "model";
  className?: string;
}) {
  const { t } = useT("agents");
  if (!override) return null;

  const lines: string[] = [];
  if (scope !== "model" && override.overridden_env_key_count > 0) {
    lines.push(
      t(($) => $.preset_override.env, {
        count: override.overridden_env_key_count,
        name: override.preset_name,
      }),
    );
  }
  if (scope !== "env" && override.model_overridden) {
    lines.push(
      t(($) => $.preset_override.model, { name: override.preset_name }),
    );
  }
  if (scope !== "env" && override.thinking_level_overridden) {
    lines.push(
      t(($) => $.preset_override.thinking_level, { name: override.preset_name }),
    );
  }
  // Nothing of this agent's is actually overridden, but the runtime is on a
  // preset — still worth saying, because it decides where this agent runs.
  if (lines.length === 0) {
    lines.push(
      t(($) => $.preset_override.applied, { name: override.preset_name }),
    );
  }

  return (
    <div
      className={`flex items-start gap-2 rounded-md border border-surface-border bg-muted/40 px-3 py-2 text-caption ${className}`}
    >
      <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      <div className="min-w-0 space-y-0.5">
        {lines.map((line) => (
          <p key={line}>{line}</p>
        ))}
        <p className="text-muted-foreground">{t(($) => $.preset_override.hint)}</p>
      </div>
    </div>
  );
}
