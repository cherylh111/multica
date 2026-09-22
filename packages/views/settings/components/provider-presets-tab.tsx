"use client";

import { useMemo, useState } from "react";
import { Loader2, Plus, ServerCog } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { useApplyProviderPresetToRuntime } from "@multica/core/runtimes/mutations";
import { workspaceProviderPresetsOptions } from "@multica/core/workspace/queries";
import {
  useCreateProviderPreset,
  useDeleteProviderPreset,
  useUpdateProviderPreset,
} from "@multica/core/workspace/mutations";
import type {
  CreateProviderPresetRequest,
  ProviderPreset,
  UpdateProviderPresetRequest,
} from "@multica/core/types";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { ProviderPresetDialog } from "./provider-preset-dialog";

/**
 * Workspace provider presets.
 *
 * A preset is the runtime-level answer to "which supplier": base URL, API key,
 * model and an optional native config fragment, applied to a runtime so every
 * agent on it inherits the same supplier on their next task. The settings page
 * owns the presets themselves; applying one is the action that makes them take
 * effect, and it lives here too so the two are never more than one screen
 * apart.
 *
 * Reads carry masked secrets only, so nothing here can display a stored key.
 */
export function ProviderPresetsTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const currentMember = useCurrentMember(wsId);
  const canManage =
    currentMember.role === "owner" || currentMember.role === "admin";

  const presetsQuery = useQuery(workspaceProviderPresetsOptions(wsId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const createPreset = useCreateProviderPreset(wsId);
  const updatePreset = useUpdateProviderPreset(wsId);
  const deletePreset = useDeleteProviderPreset(wsId);
  const applyPreset = useApplyProviderPresetToRuntime(wsId);

  const presets = useMemo(() => presetsQuery.data ?? [], [presetsQuery.data]);
  const runtimes = useMemo(() => runtimesQuery.data ?? [], [runtimesQuery.data]);
  const existingNames = useMemo(
    () => new Set(presets.map((preset) => preset.name)),
    [presets],
  );

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingPreset, setEditingPreset] = useState<ProviderPreset | null>(null);
  const [deletingPreset, setDeletingPreset] = useState<ProviderPreset | null>(null);
  const [targetPresetId, setTargetPresetId] = useState("");
  const [targetRuntimeId, setTargetRuntimeId] = useState("");
  const [applyPending, setApplyPending] = useState(false);

  const targetPreset = useMemo(
    () => presets.find((preset) => preset.id === targetPresetId) ?? null,
    [presets, targetPresetId],
  );
  // Only runtimes of the preset's own type are legal targets: env key names,
  // the native config shape and model ids are all family-specific, and the
  // server rejects a mismatch.
  const candidateRuntimes = useMemo(() => {
    if (!targetPreset) return [];
    return runtimes.filter((runtime) => runtime.provider === targetPreset.runtime_type);
  }, [runtimes, targetPreset]);

  const presetOptions = presets.map((preset) => ({
    value: preset.id,
    label: preset.name,
  }));
  const runtimeOptions = candidateRuntimes.map((runtime) => ({
    value: runtime.id,
    label: runtime.custom_name ?? runtime.name,
  }));

  const handleSave = async (
    body: CreateProviderPresetRequest | UpdateProviderPresetRequest,
  ) => {
    try {
      if (editingPreset) {
        await updatePreset.mutateAsync({
          presetId: editingPreset.id,
          ...(body as UpdateProviderPresetRequest),
        });
        toast.success(t(($) => $.provider_presets.updated_toast));
      } else {
        await createPreset.mutateAsync(body as CreateProviderPresetRequest);
        toast.success(t(($) => $.provider_presets.created_toast));
      }
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.provider_presets.save_failed_toast),
      );
      throw error;
    }
  };

  const handleDelete = async () => {
    if (!deletingPreset) return;
    try {
      await deletePreset.mutateAsync(deletingPreset.id);
      toast.success(t(($) => $.provider_presets.removed_toast));
      if (targetPresetId === deletingPreset.id) {
        setTargetPresetId("");
        setTargetRuntimeId("");
      }
      setDeletingPreset(null);
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.provider_presets.remove_failed_toast),
      );
    }
  };

  const handleApply = async () => {
    if (!targetPreset || applyPending) return;
    const runtime = candidateRuntimes.find((item) => item.id === targetRuntimeId);
    if (!runtime) return;
    setApplyPending(true);
    try {
      await applyPreset.mutateAsync({
        runtimeId: runtime.id,
        presetId: targetPreset.id,
      });
      toast.success(
        t(($) => $.provider_presets.apply_toast, {
          runtime: runtime.custom_name ?? runtime.name,
        }),
      );
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.provider_presets.apply_failed_toast),
      );
    } finally {
      setApplyPending(false);
    }
  };

  const handleClear = async (runtimeId: string, runtimeName: string) => {
    if (applyPending) return;
    setApplyPending(true);
    try {
      await applyPreset.mutateAsync({ runtimeId, presetId: null });
      toast.success(
        t(($) => $.provider_presets.cleared_toast, { runtime: runtimeName }),
      );
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.provider_presets.apply_failed_toast),
      );
    } finally {
      setApplyPending(false);
    }
  };

  return (
    <SettingsTab
      title={t(($) => $.provider_presets.title)}
      description={t(($) => $.provider_presets.description)}
    >
      <SettingsSection
        title={t(($) => $.provider_presets.presets_title)}
        description={t(($) => $.provider_presets.masked_note)}
        action={
          canManage ? (
            <Button
              size="sm"
              onClick={() => {
                setEditingPreset(null);
                setEditorOpen(true);
              }}
            >
              <Plus className="h-4 w-4" />
              {t(($) => $.provider_presets.add_preset)}
            </Button>
          ) : null
        }
      >
        <SettingsCard>
          {presetsQuery.isLoading ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
            </div>
          ) : presets.length === 0 ? (
            <div className="px-4 py-8 text-center">
              <ServerCog className="mx-auto h-5 w-5 text-muted-foreground" />
              <p className="mt-3 text-body font-medium">
                {t(($) => $.provider_presets.empty_title)}
              </p>
            </div>
          ) : (
            <ul className="divide-y divide-surface-border">
              {presets.map((preset) => {
                const usingCount = runtimes.filter(
                  (runtime) => runtime.active_provider_preset_id === preset.id,
                ).length;
                return (
                  <li key={preset.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-body font-medium">
                          {preset.name}
                        </span>
                        <Badge variant="secondary">{preset.runtime_type}</Badge>
                        {preset.enabled ? null : (
                          <Badge variant="secondary">
                            {t(($) => $.provider_presets.disabled_badge)}
                          </Badge>
                        )}
                        {usingCount > 0 ? (
                          <Badge variant="secondary">
                            {t(($) => $.provider_presets.in_use_badge, {
                              count: usingCount,
                            })}
                          </Badge>
                        ) : null}
                      </div>
                      <p className="mt-1 text-caption text-muted-foreground">
                        {preset.model ? `${t(($) => $.provider_presets.model_column)}: ${preset.model} · ` : ""}
                        {t(($) => $.provider_presets.env_column)}: {preset.env_key_count}
                      </p>
                    </div>
                    {canManage ? (
                      <div className="flex items-center gap-2">
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={t(($) => $.provider_presets.edit_aria)}
                          onClick={() => {
                            setEditingPreset(preset);
                            setEditorOpen(true);
                          }}
                        >
                          {t(($) => $.provider_presets.edit_action)}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={t(($) => $.provider_presets.remove_aria)}
                          onClick={() => setDeletingPreset(preset)}
                        >
                          {t(($) => $.provider_presets.remove_action)}
                        </Button>
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}
        </SettingsCard>
        {!canManage && !currentMember.isLoading ? (
          <p className="px-0.5 text-caption text-muted-foreground">
            {t(($) => $.provider_presets.admin_only_note)}
          </p>
        ) : null}
      </SettingsSection>

      {canManage ? (
        <SettingsSection
          title={t(($) => $.provider_presets.apply_title)}
          description={t(($) => $.provider_presets.apply_description)}
        >
          <SettingsCard>
            <div className="flex flex-wrap items-end gap-3 px-4 py-3">
              <div className="grid min-w-[12rem] flex-1 gap-2">
                <label
                  className="text-caption text-muted-foreground"
                  htmlFor="provider-preset-target-preset"
                >
                  {t(($) => $.provider_presets.presets_title)}
                </label>
                <Select
                  items={presetOptions}
                  value={targetPresetId}
                  onValueChange={(next) => {
                    if (!next) return;
                    setTargetPresetId(next);
                    setTargetRuntimeId("");
                  }}
                >
                  <SelectTrigger id="provider-preset-target-preset">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {presets.map((preset) => (
                      <SelectItem key={preset.id} value={preset.id}>
                        {preset.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid min-w-[12rem] flex-1 gap-2">
                <label
                  className="text-caption text-muted-foreground"
                  htmlFor="provider-preset-target-runtime"
                >
                  {t(($) => $.provider_presets.apply_select_label)}
                </label>
                <Select
                  items={runtimeOptions}
                  value={targetRuntimeId}
                  onValueChange={(next) => {
                    if (!next) return;
                    setTargetRuntimeId(next);
                  }}
                  disabled={!targetPreset || candidateRuntimes.length === 0}
                >
                  <SelectTrigger id="provider-preset-target-runtime">
                    <SelectValue
                      placeholder={
                        targetPreset && candidateRuntimes.length === 0
                          ? t(($) => $.provider_presets.apply_no_runtimes)
                          : t(($) => $.provider_presets.apply_select_placeholder)
                      }
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {candidateRuntimes.map((runtime) => (
                      <SelectItem key={runtime.id} value={runtime.id}>
                        {runtime.custom_name ?? runtime.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <Button
                disabled={!targetRuntimeId || applyPending}
                onClick={() => void handleApply()}
              >
                {applyPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : null}
                {t(($) => $.provider_presets.apply_action)}
              </Button>
            </div>
          </SettingsCard>
        </SettingsSection>
      ) : null}

      {canManage
        ? runtimes
            .filter((runtime) => runtime.active_provider_preset_id)
            .map((runtime) => {
              const applied = presets.find(
                (preset) => preset.id === runtime.active_provider_preset_id,
              );
              return (
                <SettingsSection
                  key={runtime.id}
                  title={runtime.custom_name ?? runtime.name}
                  description={applied?.name ?? ""}
                  action={
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={applyPending}
                      onClick={() =>
                        void handleClear(
                          runtime.id,
                          runtime.custom_name ?? runtime.name,
                        )
                      }
                    >
                      {t(($) => $.provider_presets.clear_action)}
                    </Button>
                  }
                >
                  <SettingsCard>
                    <ul className="divide-y divide-surface-border">
                      <li className="px-4 py-3 text-caption text-muted-foreground">
                        {t(($) => $.provider_presets.apply_description)}
                      </li>
                    </ul>
                  </SettingsCard>
                </SettingsSection>
              );
            })
        : null}

      <ProviderPresetDialog
        open={editorOpen}
        preset={editingPreset}
        existingNames={existingNames}
        onOpenChange={setEditorOpen}
        onSave={handleSave}
      />

      <AlertDialog
        open={deletingPreset !== null}
        onOpenChange={(open) => {
          if (!open) setDeletingPreset(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.provider_presets.delete_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.provider_presets.delete_description, {
                name: deletingPreset?.name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deletePreset.isPending}>
              {t(($) => $.provider_presets.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={deletePreset.isPending}
              onClick={(event) => {
                event.preventDefault();
                void handleDelete();
              }}
            >
              {t(($) => $.provider_presets.delete_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
