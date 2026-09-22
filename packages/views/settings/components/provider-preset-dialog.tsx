"use client";

import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import type {
  CreateProviderPresetRequest,
  ProviderPreset,
  UpdateProviderPresetRequest,
} from "@multica/core/types";
import { RUNTIME_PROFILE_RUNTIME_TYPES } from "@multica/core/types";
import { useT } from "../../i18n";

/**
 * Create / edit form for one provider preset.
 *
 * The env editor is deliberately a JSON textarea rather than a key/value grid:
 * values come back from the API masked as `***`, so a per-key field could only
 * ever show the mask. Editing the object whole keeps the round trip honest —
 * keys the user leaves as `***` are preserved server-side, and keys they retype
 * are replaced.
 *
 * `runtime_type` is editable only while creating. It is immutable on the server
 * because env key names, the native config shape and model ids are all specific
 * to one runtime family; a preset that switched families would feed one
 * supplier's config to a different CLI.
 */
export interface ProviderPresetDialogProps {
  open: boolean;
  preset: ProviderPreset | null;
  existingNames: Set<string>;
  onOpenChange: (open: boolean) => void;
  onSave: (
    body: CreateProviderPresetRequest | UpdateProviderPresetRequest,
  ) => Promise<void>;
}

interface DraftState {
  name: string;
  runtimeType: string;
  model: string;
  thinkingLevel: string;
  envText: string;
  nativeConfigText: string;
  enabled: boolean;
}

const EMPTY_DRAFT: DraftState = {
  name: "",
  runtimeType: "claude",
  model: "",
  thinkingLevel: "",
  envText: "{}",
  nativeConfigText: "{}",
  enabled: true,
};

function draftFrom(preset: ProviderPreset | null): DraftState {
  if (!preset) return { ...EMPTY_DRAFT };
  return {
    name: preset.name,
    runtimeType: preset.runtime_type,
    model: preset.model,
    thinkingLevel: preset.thinking_level,
    // Pretty-printed so the masked values are legible next to their keys.
    envText: JSON.stringify(preset.env, null, 2),
    nativeConfigText: JSON.stringify(preset.native_config, null, 2),
    enabled: preset.enabled,
  };
}

/** Parses a JSON object textarea, returning null when it is not one. */
function parseObject(raw: string): Record<string, unknown> | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return null;
  }
  return parsed as Record<string, unknown>;
}

export function ProviderPresetDialog({
  open,
  preset,
  existingNames,
  onOpenChange,
  onSave,
}: ProviderPresetDialogProps) {
  const { t } = useT("settings");
  const [draft, setDraft] = useState<DraftState>(() => draftFrom(preset));
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  // Re-seed whenever the dialog opens on a different preset; without this the
  // form would keep the previously edited preset's values.
  useEffect(() => {
    if (!open) return;
    setDraft(draftFrom(preset));
    setError("");
    setSaving(false);
  }, [open, preset]);

  const isEdit = preset !== null;
  const runtimeTypeOptions = RUNTIME_PROFILE_RUNTIME_TYPES.map((runtimeType) => ({
    value: runtimeType,
    label: runtimeType,
  }));

  const handleSave = async () => {
    if (saving) return;
    const name = draft.name.trim();
    if (name === "") {
      setError(t(($) => $.provider_presets.name_required));
      return;
    }
    if (name !== preset?.name && existingNames.has(name)) {
      setError(t(($) => $.provider_presets.name_duplicate));
      return;
    }
    const env = parseObject(draft.envText);
    if (!env) {
      setError(t(($) => $.provider_presets.env_invalid));
      return;
    }
    const nativeConfig = parseObject(draft.nativeConfigText);
    if (!nativeConfig) {
      setError(t(($) => $.provider_presets.native_config_invalid));
      return;
    }

    const shared = {
      name,
      env: env as Record<string, string>,
      model: draft.model.trim(),
      thinking_level: draft.thinkingLevel.trim(),
      native_config: nativeConfig,
      enabled: draft.enabled,
    };

    setSaving(true);
    try {
      if (isEdit) {
        await onSave(shared);
      } else {
        await onSave({ ...shared, runtime_type: draft.runtimeType });
      }
    } catch {
      // The tab surfaces the API's own message as a toast; keep the dialog
      // open so the user can fix the form rather than lose what they typed.
      setSaving(false);
      return;
    }
    setSaving(false);
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {isEdit
              ? t(($) => $.provider_presets.edit_title)
              : t(($) => $.provider_presets.create_title)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.provider_presets.masked_note)}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4 py-2">
          <div className="grid gap-2">
            <Label htmlFor="provider-preset-name">
              {t(($) => $.provider_presets.name_label)}
            </Label>
            <Input
              id="provider-preset-name"
              value={draft.name}
              placeholder={t(($) => $.provider_presets.name_placeholder)}
              onChange={(event) => {
                setDraft((prev) => ({ ...prev, name: event.target.value }));
                setError("");
              }}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="provider-preset-runtime">
              {t(($) => $.provider_presets.runtime_type_label)}
            </Label>
            {isEdit ? (
              <Input id="provider-preset-runtime" value={draft.runtimeType} disabled />
            ) : (
              <Select
                items={runtimeTypeOptions}
                value={draft.runtimeType}
                onValueChange={(next) => {
                  if (!next) return;
                  setDraft((prev) => ({ ...prev, runtimeType: next }));
                }}
              >
                <SelectTrigger id="provider-preset-runtime">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {RUNTIME_PROFILE_RUNTIME_TYPES.map((runtimeType) => (
                    <SelectItem key={runtimeType} value={runtimeType}>
                      {runtimeType}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            <p className="text-caption text-muted-foreground">
              {t(($) => $.provider_presets.runtime_type_help)}
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="provider-preset-model">
                {t(($) => $.provider_presets.model_label)}
              </Label>
              <Input
                id="provider-preset-model"
                value={draft.model}
                placeholder={t(($) => $.provider_presets.model_placeholder)}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, model: event.target.value }))
                }
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="provider-preset-thinking">
                {t(($) => $.provider_presets.thinking_level_label)}
              </Label>
              <Input
                id="provider-preset-thinking"
                value={draft.thinkingLevel}
                placeholder={t(($) => $.provider_presets.thinking_level_placeholder)}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    thinkingLevel: event.target.value,
                  }))
                }
              />
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="provider-preset-env">
              {t(($) => $.provider_presets.env_label)}
            </Label>
            <Textarea
              id="provider-preset-env"
              className="font-mono text-caption"
              rows={5}
              value={draft.envText}
              placeholder={t(($) => $.provider_presets.env_hint)}
              onChange={(event) => {
                setDraft((prev) => ({ ...prev, envText: event.target.value }));
                setError("");
              }}
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.provider_presets.env_hint)}
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="provider-preset-native">
              {t(($) => $.provider_presets.native_config_label)}
            </Label>
            <Textarea
              id="provider-preset-native"
              className="font-mono text-caption"
              rows={4}
              value={draft.nativeConfigText}
              placeholder="{}"
              onChange={(event) => {
                setDraft((prev) => ({
                  ...prev,
                  nativeConfigText: event.target.value,
                }));
                setError("");
              }}
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.provider_presets.native_config_hint)}
            </p>
          </div>

          <div className="flex items-center justify-between gap-3">
            <Label htmlFor="provider-preset-enabled">
              {t(($) => $.provider_presets.enabled_label)}
            </Label>
            <Switch
              id="provider-preset-enabled"
              checked={draft.enabled}
              onCheckedChange={(checked) =>
                setDraft((prev) => ({ ...prev, enabled: checked }))
              }
            />
          </div>

          {error ? <p className="text-caption text-destructive">{error}</p> : null}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t(($) => $.provider_presets.cancel)}
          </Button>
          <Button onClick={() => void handleSave()} disabled={saving}>
            {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
            {t(($) => $.provider_presets.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
