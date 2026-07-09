import { useEffect, useMemo, useState } from "react";
import {
  APIRequestError,
  type APIErrorShape,
  requestJSON,
  requestJSONAllowHTTPError,
  sendJSON,
} from "../lib/api";
import type {
  AutostartDetectResponse,
  BootstrapState,
  ClaudeProfilesResponse,
  ClaudeProfileSummary,
  CodexProvidersResponse,
  CodexProviderSummary,
  FeishuAppAutoConfigApplyResponse,
  FeishuAppAutoConfigPlan,
  FeishuAppAutoConfigPlanResponse,
  FeishuAppAutoConfigPublishResponse,
  FeishuAppAutoConfigRequirementStatus,
  FeishuAppResponse,
  FeishuAppSummary,
  FeishuAppsResponse,
  ImageStagingCleanupResponse,
  ImageStagingStatusResponse,
  LogsStorageCleanupResponse,
  LogsStorageStatusResponse,
  PreviewDriveCleanupResponse,
  PreviewDriveStatusResponse,
  RuntimePeerSurfaceStatus,
  RuntimeSurfaceStatus,
  RuntimeStatus,
  RuntimeWeComStatus,
  WeComBotResponse,
  WeComBotsResponse,
  WeComBotSummary,
  WeComBotWriteRequest,
  VSCodeDetectResponse,
} from "../lib/types";
import {
  blankToUndefined,
  loadAutostartState,
  loadVSCodeState,
  readAPIError,
  vscodeApplyModeForScenario,
  vscodeIsReady,
} from "./shared/helpers";
import {
  autoConfigNoticeTone,
  describeAutoConfigBlockingReason,
  describeAutoConfigHeadline,
  describeAutoConfigRequirementDisplay,
  describeAutoConfigSummary,
  describeAutoConfigTag,
} from "./shared/feishuAutoConfig";
import {
  resolveRuntimeApplyFailureTarget,
  runAutoConfigMutation,
  saveAndVerifyFeishuApp,
  useQRCodeOnboardingFlow,
} from "./shared/feishuFlow";
import { runAdminStorageCleanup } from "./shared/adminStorage";
import { ClaudeProfileSection } from "./admin/ClaudeProfileSection";
import { CodexProviderSection } from "./admin/CodexProviderSection";

type NoticeTone = "good" | "warn" | "danger";

type DetailNotice = {
  tone: NoticeTone;
  message: string;
};

type AutoConfigState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; data: FeishuAppAutoConfigPlanResponse }
  | { status: "error"; message: string };

type NewRobotForm = {
  name: string;
  appId: string;
  appSecret: string;
};

type WeComBotForm = {
  id: string;
  name: string;
  botId: string;
  secret: string;
  enabled: boolean;
};

const newRobotID = "new";
const emptyWeComBotForm: WeComBotForm = {
  id: "",
  name: "",
  botId: "",
  secret: "",
  enabled: true,
};

export function AdminRoute() {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [bootstrap, setBootstrap] = useState<BootstrapState | null>(null);
  const [apps, setApps] = useState<FeishuAppSummary[]>([]);
  const [selectedRobotID, setSelectedRobotID] = useState(newRobotID);
  const [autoConfigPlans, setAutoConfigPlans] = useState<Record<string, AutoConfigState>>(
    {},
  );
  const [detailNotice, setDetailNotice] = useState<DetailNotice | null>(null);
  const [codexProviders, setCodexProviders] = useState<CodexProviderSummary[]>([]);
  const [codexProvidersError, setCodexProvidersError] = useState("");
  const [claudeProfiles, setClaudeProfiles] = useState<ClaudeProfileSummary[]>([]);
  const [claudeProfilesError, setClaudeProfilesError] = useState("");
  const [newRobotForm, setNewRobotForm] = useState<NewRobotForm>({
    name: "",
    appId: "",
    appSecret: "",
  });
  const [autostart, setAutostart] = useState<AutostartDetectResponse | null>(
    null,
  );
  const [autostartError, setAutostartError] = useState("");
  const [vscode, setVSCode] = useState<VSCodeDetectResponse | null>(null);
  const [vscodeError, setVSCodeError] = useState("");
  const [imageStaging, setImageStaging] =
    useState<ImageStagingStatusResponse | null>(null);
  const [imageStagingError, setImageStagingError] = useState("");
  const [logsStorage, setLogsStorage] = useState<LogsStorageStatusResponse | null>(
    null,
  );
  const [logsStorageError, setLogsStorageError] = useState("");
  const [previewMap, setPreviewMap] = useState<
    Record<string, PreviewDriveStatusResponse>
  >({});
  const [previewError, setPreviewError] = useState("");
  const [runtimeStatus, setRuntimeStatus] = useState<RuntimeStatus | null>(null);
  const [runtimeStatusError, setRuntimeStatusError] = useState("");
  const [wecomBots, setWeComBots] = useState<WeComBotSummary[]>([]);
  const [wecomBotsError, setWeComBotsError] = useState("");
  const [wecomBotForm, setWeComBotForm] = useState<WeComBotForm>(emptyWeComBotForm);
  const [editingWeComBotID, setEditingWeComBotID] = useState<string | null>(null);
  const [actionBusy, setActionBusy] = useState("");
  const [deleteTargetID, setDeleteTargetID] = useState<string | null>(null);
  const [publishTargetID, setPublishTargetID] = useState<string | null>(null);

  const selectedApp = useMemo(
    () => apps.find((app) => app.id === selectedRobotID) ?? null,
    [apps, selectedRobotID],
  );
  const selectedAutoConfig: AutoConfigState = selectedApp
    ? autoConfigPlans[selectedApp.id] || { status: "idle" }
    : { status: "idle" };
  const versionTitle = buildAdminPageTitle(bootstrap);
  const previewSummary = useMemo(() => {
    return Object.values(previewMap).reduce(
      (accumulator, item) => {
        accumulator.fileCount += item.summary.fileCount;
        accumulator.bytes += item.summary.estimatedBytes;
        return accumulator;
      },
      { fileCount: 0, bytes: 0 },
    );
  }, [previewMap]);
  const {
    connectMode,
    connectError,
    onboardingSession,
    changeConnectMode,
    clearConnectError,
    completeQRCodeSession,
    resetConnectFlow,
  } = useQRCodeOnboardingFlow({
    enabled: selectedRobotID === newRobotID,
    actionBusy,
    setActionBusy,
    sessionsPath: "/api/admin/feishu/onboarding/sessions",
    onCompleteSuccess: async (appID) => {
      await loadAdminPage({ preferredRobotID: appID });
      setSelectedRobotID(appID);
      setDetailNotice({ tone: "good", message: "已完成连接验证。" });
    },
    resetSessionOnSuccess: true,
  });

  useEffect(() => {
    document.title = versionTitle;
  }, [versionTitle]);

  useEffect(() => {
    void loadAdminPage().catch(() => {
      setLoadError("当前页面暂时无法读取状态，请刷新后重试。");
      setLoading(false);
    });
  }, []);

  useEffect(() => {
    setAutoConfigPlans((current) => {
      const next: Record<string, AutoConfigState> = {};
      for (const app of apps) {
        next[app.id] = current[app.id] || { status: "idle" };
      }
      return next;
    });
  }, [apps]);

  useEffect(() => {
    if (!selectedApp?.id) {
      return;
    }
    if (selectedAutoConfig.status !== "idle") {
      return;
    }
    void loadAutoConfigPlan(selectedApp.id);
  }, [selectedApp?.id, selectedAutoConfig.status]);

  useEffect(() => {
    setPublishTargetID(null);
    if (selectedRobotID === newRobotID) {
      return;
    }
    resetConnectFlow();
  }, [selectedRobotID]);

  async function loadAdminPage(options?: { preferredRobotID?: string }) {
    setLoading(true);
    setLoadError("");

    const [
      bootstrapState,
      appList,
      codexProvidersResult,
      claudeProfilesResult,
      runtimeStatusResult,
      wecomBotsResult,
      autostartState,
      vscodeState,
      imageResult,
      logsResult,
    ] = await Promise.all([
      requestJSON<BootstrapState>("/api/admin/bootstrap-state"),
      requestJSON<FeishuAppsResponse>("/api/admin/feishu/apps"),
      safeRequest<CodexProvidersResponse>("/api/admin/codex/providers"),
      safeRequest<ClaudeProfilesResponse>("/api/admin/claude/profiles"),
      safeRequest<RuntimeStatus>("/api/admin/runtime-status"),
      safeRequest<WeComBotsResponse>("/api/admin/wecom/bots"),
      loadAutostartState("/api/admin/autostart/detect"),
      loadVSCodeState("/api/admin/vscode/detect"),
      safeRequest<ImageStagingStatusResponse>("/api/admin/storage/image-staging"),
      safeRequest<LogsStorageStatusResponse>("/api/admin/storage/logs"),
    ]);

    const previewResults = await Promise.allSettled(
      appList.apps.map(async (app) => {
        const data = await requestJSON<PreviewDriveStatusResponse>(
          `/api/admin/storage/preview-drive/${encodeURIComponent(app.id)}`,
        );
        return [app.id, data] as const;
      }),
    );

    const previews: Record<string, PreviewDriveStatusResponse> = {};
    let previewFailed = false;
    previewResults.forEach((result) => {
      if (result.status === "fulfilled") {
        previews[result.value[0]] = result.value[1];
        return;
      }
      previewFailed = true;
    });

    const nextSelectedRobotID =
      appList.apps.find((app) => app.id === options?.preferredRobotID)?.id ||
      appList.apps.find((app) => app.id === selectedRobotID)?.id ||
      appList.apps[0]?.id ||
      newRobotID;

    setBootstrap(bootstrapState);
    setApps(appList.apps);
    setSelectedRobotID(nextSelectedRobotID);
    setCodexProviders(codexProvidersResult.data?.providers || []);
    setCodexProvidersError(codexProvidersResult.error);
    setClaudeProfiles(claudeProfilesResult.data?.profiles || []);
    setClaudeProfilesError(claudeProfilesResult.error);
    setRuntimeStatus(runtimeStatusResult.data || null);
    setRuntimeStatusError(runtimeStatusResult.error);
    setWeComBots(wecomBotsResult.data?.bots || []);
    setWeComBotsError(wecomBotsResult.error);
    setAutostart(autostartState.data);
    setAutostartError(autostartState.error);
    setVSCode(vscodeState.data);
    setVSCodeError(vscodeState.error);
    setImageStaging(imageResult.data);
    setImageStagingError(imageResult.error);
    setLogsStorage(logsResult.data);
    setLogsStorageError(logsResult.error);
    setPreviewMap(previews);
    setPreviewError(previewFailed ? "部分预览文件状态暂时没有读取成功。" : "");
    setLoading(false);
  }

  async function loadAutoConfigPlan(appID: string) {
    setAutoConfigPlans((current) => ({
      ...current,
      [appID]: { status: "loading" },
    }));
    try {
      const response = await requestJSONAllowHTTPError<
        FeishuAppAutoConfigPlanResponse | APIErrorShape
      >(`/api/admin/feishu/apps/${encodeURIComponent(appID)}/auto-config/plan`);
      if (!response.ok) {
        const payload = readAPIError(response);
        setAutoConfigPlans((current) => ({
          ...current,
          [appID]: {
            status: "error",
            message:
              payload?.code === "feishu_app_runtime_unavailable"
                ? "当前机器人还在同步运行设置，请稍后再检查自动配置。"
                : "当前还没有读取到自动配置状态，请稍后重试。",
          },
        }));
        return;
      }
      const payload = response.data as FeishuAppAutoConfigPlanResponse;
      setApps((current) =>
        current.map((app) => (app.id === payload.app.id ? payload.app : app)),
      );
      setAutoConfigPlans((current) => ({
        ...current,
        [appID]: { status: "ready", data: payload },
      }));
    } catch {
      setAutoConfigPlans((current) => ({
        ...current,
        [appID]: {
          status: "error",
          message: "当前还没有读取到自动配置状态，请稍后重试。",
        },
      }));
    }
  }

  async function createRobot() {
    if (!newRobotForm.appId.trim() || !newRobotForm.appSecret.trim()) {
      setDetailNotice({
        tone: "danger",
        message: "请填写完整的 App ID 和 App Secret。",
      });
      return;
    }

    setActionBusy("create-robot");
    try {
      const result = await saveAndVerifyFeishuApp({
        save: async () => {
          const saved = await sendJSON<FeishuAppResponse>("/api/admin/feishu/apps", "POST", {
            name: blankToUndefined(newRobotForm.name),
            appId: blankToUndefined(newRobotForm.appId),
            appSecret: blankToUndefined(newRobotForm.appSecret),
            enabled: true,
          });
          return saved.app.id;
        },
        verifyPath: (appID) =>
          `/api/admin/feishu/apps/${encodeURIComponent(appID)}/verify`,
        reload: (appID) => loadAdminPage({ preferredRobotID: appID }),
      });
      setSelectedRobotID(result.appID);
      if (!result.verified) {
        setDetailNotice({
          tone: "danger",
          message: "连接验证没有通过，请检查 App ID 和 App Secret 后重试。",
        });
        return;
      }
      setDetailNotice({ tone: "good", message: "已完成连接验证。" });
      setNewRobotForm({ name: "", appId: "", appSecret: "" });
    } catch (error: unknown) {
      if (await maybeRecoverRuntimeApplyFailure(error)) {
        return;
      }
      setDetailNotice({ tone: "danger", message: "当前还不能保存这个机器人，请稍后重试。" });
    } finally {
      setActionBusy("");
    }
  }

  async function maybeRecoverRuntimeApplyFailure(error: unknown): Promise<boolean> {
    const appID = resolveRuntimeApplyFailureTarget(error);
    if (!appID) {
      return false;
    }
    await loadAdminPage({
      preferredRobotID: appID,
    });
    setSelectedRobotID(appID);
    setDetailNotice({
      tone: "warn",
      message:
        "配置已经保存，但当前运行中的机器人还没有同步完成。请稍后刷新状态后再继续。",
    });
    return true;
  }

  async function refreshWeComBots() {
    const result = await safeRequest<WeComBotsResponse>("/api/admin/wecom/bots");
    setWeComBots(result.data?.bots || []);
    setWeComBotsError(result.error);
  }

  function resetWeComBotForm() {
    setWeComBotForm(emptyWeComBotForm);
    setEditingWeComBotID(null);
  }

  function startEditWeComBot(bot: WeComBotSummary) {
    setEditingWeComBotID(bot.id);
    setWeComBotForm({
      id: bot.id,
      name: bot.name || "",
      botId: bot.botId || "",
      secret: "",
      enabled: bot.enabled,
    });
    setDetailNotice(null);
  }

  async function saveWeComBot() {
    if (!wecomBotForm.botId.trim()) {
      setDetailNotice({ tone: "danger", message: "请填写完整的企微 Bot ID。" });
      return;
    }
    if (!editingWeComBotID && !wecomBotForm.secret.trim()) {
      setDetailNotice({ tone: "danger", message: "新增企微机器人时必须填写 Secret。" });
      return;
    }
    const busyKey = editingWeComBotID ? `update-wecom-${editingWeComBotID}` : "create-wecom-bot";
    setActionBusy(busyKey);
    try {
      const payload: WeComBotWriteRequest = {
        id: blankToUndefined(wecomBotForm.id),
        name: blankToUndefined(wecomBotForm.name),
        botId: blankToUndefined(wecomBotForm.botId),
        enabled: wecomBotForm.enabled,
      };
      if (wecomBotForm.secret.trim()) {
        payload.secret = blankToUndefined(wecomBotForm.secret);
      }
      if (editingWeComBotID) {
        await sendJSON<WeComBotResponse>(
          `/api/admin/wecom/bots/${encodeURIComponent(editingWeComBotID)}`,
          "PUT",
          payload,
        );
      } else {
        await sendJSON<WeComBotResponse>("/api/admin/wecom/bots", "POST", payload);
      }
      await refreshWeComBots();
      await loadAdminPage({ preferredRobotID: selectedRobotID });
      resetWeComBotForm();
      setDetailNotice({
        tone: "good",
        message: editingWeComBotID ? "企微机器人配置已更新。" : "企微机器人已创建并接入运行态。",
      });
    } catch {
      setDetailNotice({
        tone: "danger",
        message: editingWeComBotID ? "当前还不能更新这个企微机器人，请稍后重试。" : "当前还不能保存这个企微机器人，请稍后重试。",
      });
    } finally {
      setActionBusy("");
    }
  }

  async function reconnectWeComBot(botID: string) {
    setActionBusy(`reconnect-wecom-${botID}`);
    try {
      await sendJSON<WeComBotResponse>(
        `/api/admin/wecom/bots/${encodeURIComponent(botID)}/reconnect`,
        "POST",
      );
      await refreshWeComBots();
      await loadAdminPage({ preferredRobotID: selectedRobotID });
      setDetailNotice({ tone: "good", message: "已请求企微机器人重连。" });
    } catch {
      setDetailNotice({ tone: "danger", message: "当前还不能重连这个企微机器人，请稍后重试。" });
    } finally {
      setActionBusy("");
    }
  }

  async function deleteWeComBot(botID: string) {
    setActionBusy(`delete-wecom-${botID}`);
    try {
      await sendJSON<WeComBotResponse>(
        `/api/admin/wecom/bots/${encodeURIComponent(botID)}`,
        "DELETE",
      );
      await refreshWeComBots();
      await loadAdminPage({ preferredRobotID: selectedRobotID });
      if (editingWeComBotID === botID) {
        resetWeComBotForm();
      }
      setDetailNotice({ tone: "good", message: "企微机器人已删除。" });
    } catch {
      setDetailNotice({ tone: "danger", message: "当前还不能删除这个企微机器人，请稍后重试。" });
    } finally {
      setActionBusy("");
    }
  }

  function syncAppSummary(app: FeishuAppSummary) {
    setApps((current) => current.map((item) => (item.id === app.id ? app : item)));
  }

  function syncAutoConfigPlan(app: FeishuAppSummary, plan: FeishuAppAutoConfigPlan) {
    syncAppSummary(app);
    setAutoConfigPlans((current) => ({
      ...current,
      [app.id]: {
        status: "ready",
        data: {
          app,
          plan,
        },
      },
    }));
  }

  async function applyAutoConfig() {
    if (!selectedApp?.id) {
      return;
    }
    setActionBusy("auto-config-apply");
    try {
      const result = await runAutoConfigMutation<FeishuAppAutoConfigApplyResponse>({
        path: `/api/admin/feishu/apps/${encodeURIComponent(selectedApp.id)}/auto-config/apply`,
        init: { method: "POST" },
        fallbackErrorMessage: "自动补齐没有完成，请稍后重试。",
        fallbackSuccessMessage: "自动配置状态已更新。",
      });
      if (!result.ok) {
        setDetailNotice({
          tone: "danger",
          message: result.message,
        });
        return;
      }
      syncAutoConfigPlan(result.payload.app, result.payload.result.plan);
      setDetailNotice(result.notice);
    } catch {
      setDetailNotice({
        tone: "danger",
        message: "自动补齐没有完成，请稍后重试。",
      });
    } finally {
      setActionBusy("");
    }
  }

  async function publishAutoConfig() {
    if (!selectedApp?.id) {
      return;
    }
    setActionBusy("auto-config-publish");
    try {
      const result = await runAutoConfigMutation<FeishuAppAutoConfigPublishResponse>({
        path: `/api/admin/feishu/apps/${encodeURIComponent(selectedApp.id)}/auto-config/publish`,
        init: {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify({}),
        },
        fallbackErrorMessage: "提交发布没有成功，请稍后重试。",
        fallbackSuccessMessage: "发布状态已更新。",
      });
      if (!result.ok) {
        setDetailNotice({
          tone: "danger",
          message: result.message,
        });
        return;
      }
      syncAutoConfigPlan(result.payload.app, result.payload.result.plan);
      setDetailNotice(result.notice);
      setPublishTargetID(null);
    } catch {
      setDetailNotice({
        tone: "danger",
        message: "提交发布没有成功，请稍后重试。",
      });
    } finally {
      setActionBusy("");
    }
  }

  async function deleteRobot() {
    if (!deleteTargetID) {
      return;
    }
    setActionBusy("delete-robot");
    try {
      const response = await requestJSONAllowHTTPError<unknown>(
        `/api/admin/feishu/apps/${encodeURIComponent(deleteTargetID)}`,
        { method: "DELETE" },
      );
      if (!response.ok) {
        throw new APIRequestError(
          response.status,
          "delete failed",
          readAPIError(response)?.code,
          readAPIError(response)?.details,
        );
      }
      await loadAdminPage();
      setDetailNotice({ tone: "good", message: "机器人已删除。" });
      setDeleteTargetID(null);
    } catch (error: unknown) {
      if (await maybeRecoverRuntimeApplyFailure(error)) {
        setDeleteTargetID(null);
        return;
      }
      setDetailNotice({ tone: "danger", message: "当前还不能删除这个机器人，请稍后重试。" });
    } finally {
      setActionBusy("");
      setDeleteTargetID(null);
    }
  }

  async function enableAutostart() {
    setActionBusy("autostart");
    try {
      const response = await sendJSON<AutostartDetectResponse>(
        "/api/admin/autostart/apply",
        "POST",
      );
      setAutostart(response);
      setAutostartError("");
    } catch {
      setAutostartError("自动运行设置暂时没有更新成功。");
    } finally {
      setActionBusy("");
    }
  }

  async function repairVSCode() {
    setActionBusy("vscode");
    try {
      if (!vscode) {
        await loadAdminPage({ preferredRobotID: selectedRobotID });
        return;
      }
      if (vscode.needsShimReinstall && vscode.latestBundleEntrypoint) {
        const response = await sendJSON<VSCodeDetectResponse>(
          "/api/admin/vscode/reinstall-shim",
          "POST",
          { bundleEntrypoint: vscode.latestBundleEntrypoint },
        );
        setVSCode(response);
        setVSCodeError("");
        return;
      }
      const mode = vscodeApplyModeForScenario(vscode, "current_machine");
      const response = await sendJSON<VSCodeDetectResponse>(
        "/api/admin/vscode/apply",
        "POST",
        {
          mode: mode || "managed_shim",
          bundleEntrypoint: vscode.latestBundleEntrypoint,
        },
      );
      setVSCode(response);
      setVSCodeError("");
    } catch {
      setVSCodeError("VS Code 集成暂时没有更新成功。");
    } finally {
      setActionBusy("");
    }
  }

  async function cleanupImageStaging() {
    await runAdminStorageCleanup({
      busyKey: "cleanup-image",
      setActionBusy,
      request: () =>
        sendJSON<ImageStagingCleanupResponse>(
          "/api/admin/storage/image-staging/cleanup",
          "POST",
        ),
      onSuccess: (response) => {
        setImageStaging((current) =>
          current
            ? {
                ...current,
                fileCount: response.remainingFileCount,
                totalBytes: response.remainingBytes,
              }
            : current,
        );
        setImageStagingError("");
      },
      onError: () => {
        setImageStagingError("图片暂存清理没有完成，请稍后重试。");
      },
    });
  }

  async function cleanupLogsStorage() {
    await runAdminStorageCleanup({
      busyKey: "cleanup-logs",
      setActionBusy,
      request: () =>
        sendJSON<LogsStorageCleanupResponse>(
          "/api/admin/storage/logs/cleanup",
          "POST",
          { olderThanHours: 24 },
        ),
      onSuccess: (response) => {
        setLogsStorage((current) =>
          current
            ? {
                ...current,
                fileCount: response.remainingFileCount,
                totalBytes: response.remainingBytes,
              }
            : current,
        );
        setLogsStorageError("");
      },
      onError: () => {
        setLogsStorageError("日志清理没有完成，请稍后重试。");
      },
    });
  }

  async function cleanupPreviewDrive() {
    if (apps.length === 0) {
      return;
    }
    await runAdminStorageCleanup({
      busyKey: "cleanup-preview",
      setActionBusy,
      request: () =>
        Promise.allSettled(
          apps.map((app) =>
            sendJSON<PreviewDriveCleanupResponse>(
              `/api/admin/storage/preview-drive/${encodeURIComponent(app.id)}/cleanup`,
              "POST",
            ),
          ),
        ),
      onSuccess: (results) => {
        const nextMap: Record<string, PreviewDriveStatusResponse> = { ...previewMap };
        let failed = false;
        results.forEach((result) => {
          if (result.status !== "fulfilled") {
            failed = true;
            return;
          }
          nextMap[result.value.gatewayId] = {
            gatewayId: result.value.gatewayId,
            name: result.value.name,
            summary: result.value.result.summary,
          };
        });
        setPreviewMap(nextMap);
        setPreviewError(failed ? "部分预览文件暂时没有清理成功。" : "");
      },
      onError: () => {
        setPreviewError("预览文件清理没有完成，请稍后重试。");
      },
    });
  }

  function renderRequirementSection(
    title: string,
    requirements: FeishuAppAutoConfigRequirementStatus[],
  ) {
    if (requirements.length === 0) {
      return null;
    }
    return (
      <div className="detail-stack">
        <strong>{title}</strong>
        <div className="detail-stack">
          {requirements.map((item) => {
            const display = describeAutoConfigRequirementDisplay(item);
            return (
              <p key={`${item.kind}:${item.key}`} className="support-copy">
                <strong>{display.label}</strong>
                {display.detail ? `：${display.detail}` : ""}
              </p>
            );
          })}
        </div>
      </div>
    );
  }

  function renderRuntimePanel() {
    const statuses = runtimeStatus?.surfaceStatuses || [];
    const instances = runtimeStatus?.instanceStatuses || [];
    const gateways = runtimeStatus?.gateways || [];
    const wecomBots = runtimeStatus?.wecomBots || [];
    const recentFailures = runtimeStatus?.recentFailures || [];
    const activeRemoteTurns = runtimeStatus?.activeRemoteTurns || [];
    const pendingRemoteTurns = runtimeStatus?.pendingRemoteTurns || [];
    const successRate = runtimeStatus?.deliverySuccessRate || 0;
    const successCount = runtimeStatus?.deliverySuccessCount || 0;
    const failureCount = runtimeStatus?.deliveryFailureCount || 0;

    return (
      <section className="panel">
        <div className="step-stage-head">
          <h2>运行态总览</h2>
          <p>实例、入口、队列、重连与最近失败</p>
        </div>
        <div className="soft-grid" style={{ marginTop: "1rem" }}>
          <article className="soft-card-v2">
            <h4>入口与队列</h4>
            <p>
              {runtimeStatus
                ? `已接管 ${runtimeStatus.attachedSurfaceCount || 0} 个入口，排队 ${runtimeStatus.queuedMessageCount || 0} 条消息。`
                : "暂未读取到运行态摘要。"}
            </p>
            <div className="definition-list">
              <div>
                <dt>Surface</dt>
                <dd>{statuses.length}</dd>
              </div>
              <div>
                <dt>待投递请求</dt>
                <dd>{runtimeStatus?.pendingRequestCount || 0}</dd>
              </div>
              <div>
                <dt>远端进行中</dt>
                <dd>{activeRemoteTurns.length}</dd>
              </div>
              <div>
                <dt>远端待派发</dt>
                <dd>{pendingRemoteTurns.length}</dd>
              </div>
            </div>
          </article>
          <article className="soft-card-v2">
            <h4>投递与 Gateway</h4>
            <p>
              {runtimeStatus
                ? `连接 ${runtimeStatus.connectedGatewayCount || 0}，降级 ${runtimeStatus.degradedGatewayCount || 0}，离线 ${runtimeStatus.offlineGatewayCount || 0}。`
                : "暂未读取到 gateway 状态。"}
            </p>
            <div className="definition-list">
              <div>
                <dt>成功率</dt>
                <dd>{formatPercent(successRate)}</dd>
              </div>
              <div>
                <dt>成功 / 失败</dt>
                <dd>{`${successCount} / ${failureCount}`}</dd>
              </div>
              <div>
                <dt>在线实例</dt>
                <dd>{runtimeStatus?.onlineInstanceCount || 0}</dd>
              </div>
              <div>
                <dt>Managed 实例</dt>
                <dd>{runtimeStatus?.managedInstanceCount || 0}</dd>
              </div>
              <div>
                <dt>需重投递</dt>
                <dd>{runtimeStatus?.redeliveryRequestCount || 0}</dd>
              </div>
              <div>
                <dt>Gateway 总数</dt>
                <dd>{gateways.length}</dd>
              </div>
            </div>
          </article>
        </div>
        {runtimeStatusError ? (
          <div className="notice-banner warn" style={{ marginTop: "1rem" }}>
            {runtimeStatusError}
          </div>
        ) : null}
        <div className="card-grid card-grid-two-column" style={{ marginTop: "1rem" }}>
          <article className="soft-card-v2">
            <h4>企微通道</h4>
            {wecomBots.length === 0 ? (
              <p>当前没有企微运行态。</p>
            ) : (
              <div className="detail-stack">
                {wecomBots.map((wecom) => (
                  <div key={wecom.gatewayId || wecom.name || "wecom"} className="admin-subpanel">
                    <div className="inline-status-row">
                      <strong>{wecom.name || wecom.gatewayId || "WeCom Bot"}</strong>
                      <span className={`status-badge ${wecomStateTone(wecom)}`}>
                        {wecomStateLabel(wecom)}
                      </span>
                    </div>
                    <p className="support-copy">
                      {wecom.lastError?.trim()
                        ? `最近失败：${wecom.lastError}`
                        : wecom.lastConnectedAt
                          ? `最近连通：${formatTimestamp(wecom.lastConnectedAt)}`
                          : "还没有连通记录"}
                    </p>
                    <div className="definition-list">
                      <div>
                        <dt>重连次数</dt>
                        <dd>{wecom.reconnectTries}</dd>
                      </div>
                      <div>
                        <dt>下一次重试</dt>
                        <dd>{wecom.nextRetryAt ? formatTimestamp(wecom.nextRetryAt) : "无"}</dd>
                      </div>
                      <div>
                        <dt>文件发送</dt>
                        <dd>{wecom.capabilities.fileSend ? "已支持" : "未支持"}</dd>
                      </div>
                      <div>
                        <dt>按钮上限</dt>
                        <dd>{wecom.capabilities.maxButtons || 0}</dd>
                      </div>
                    </div>
                    <div className="button-row">
                      <button
                        className="secondary-button"
                        type="button"
                        disabled={!wecom.gatewayId || actionBusy === `reconnect-wecom-${wecom.gatewayId}`}
                        onClick={() => {
                          const botID = (wecom.gatewayId || "").replace(/^wecom:/, "");
                          if (botID) {
                            void reconnectWeComBot(botID);
                          }
                        }}
                      >
                        重新连接
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </article>
          <article className="soft-card-v2">
            <h4>Gateway 状态</h4>
            {gateways.length === 0 ? (
              <p>当前没有可见 gateway。</p>
            ) : (
              <div className="detail-stack">
                {gateways.map((gateway) => (
                  <div key={gateway.gatewayId} className="admin-subpanel">
                    <div className="inline-status-row">
                      <strong>{gateway.name || gateway.gatewayId}</strong>
                      <span
                        className={`status-badge ${gatewayStateTone(gateway.state, gateway.disabled)}`}
                      >
                        {gatewayStateLabel(gateway.state, gateway.disabled)}
                      </span>
                    </div>
                    <p className="support-copy">
                      {gateway.lastError?.trim()
                        ? gateway.lastError
                        : gateway.lastConnectedAt
                          ? `最近连接：${formatTimestamp(gateway.lastConnectedAt)}`
                          : "还没有连接记录"}
                    </p>
                  </div>
                ))}
              </div>
            )}
          </article>
        </div>
        <div className="card-grid card-grid-two-column" style={{ marginTop: "1rem" }}>
          <article className="soft-card-v2">
            <h4>最近失败</h4>
            {recentFailures.length === 0 ? (
              <p>当前没有记录到最近失败。</p>
            ) : (
              <div className="detail-stack">
                {recentFailures.map((failure, index) => (
                  <div
                    key={`${failure.occurredAt}-${failure.surfaceSessionId || failure.gatewayId || index}`}
                    className="admin-subpanel"
                  >
                    <div className="inline-status-row">
                      <strong>{failure.reason || "unknown failure"}</strong>
                      <span className="status-badge warn">{failure.channel || "unknown"}</span>
                    </div>
                    <p className="support-copy">
                      {[failure.gatewayId || "", failure.surfaceSessionId || "", failure.eventKind || ""]
                        .filter(Boolean)
                        .join(" · ")}
                    </p>
                    <p className="support-copy">{formatTimestamp(failure.occurredAt)}</p>
                  </div>
                ))}
              </div>
            )}
          </article>
          <article className="soft-card-v2">
            <h4>实例状态</h4>
            {instances.length === 0 ? (
              <p>当前没有实例摘要。</p>
            ) : (
              <div className="detail-stack">
                {instances.map((instance) => (
                  <div key={instance.instanceId} className="admin-subpanel">
                    <div className="inline-status-row">
                      <strong>{instance.displayName || instance.instanceId}</strong>
                      <span
                        className={`status-badge ${instance.online ? "good" : "warn"}`}
                      >
                        {instance.status || (instance.online ? "online" : "offline")}
                      </span>
                    </div>
                    <p className="support-copy">
                      {instance.workspaceRoot || "未绑定工作区"}
                    </p>
                    <p className="support-copy">
                      {instance.lastError?.trim()
                        ? `最近失败：${instance.lastError}`
                        : instance.lastHelloAt
                          ? `最近心跳：${formatTimestamp(instance.lastHelloAt)}`
                          : "还没有心跳记录"}
                    </p>
                  </div>
                ))}
              </div>
            )}
          </article>
        </div>
        <div className="detail-stack" style={{ marginTop: "1rem" }}>
          <h4 style={{ margin: 0 }}>入口视图</h4>
          {statuses.length === 0 ? (
            <p className="support-copy">当前没有 surface 运行态。</p>
          ) : (
            <div className="detail-stack">
              {statuses.map((surface) => (
                <article key={surface.surfaceSessionId} className="soft-card-v2">
                  <div className="inline-status-row">
                    <strong>{surface.displayTitle}</strong>
                    <span
                      className={`status-badge ${surfaceTone(surface)}`}
                    >
                      {surfaceBadgeLabel(surface)}
                    </span>
                  </div>
                  <p className="support-copy">
                    {[
                      surface.platform || "unknown",
                      surface.ownerSurface ? "owner" : "shared",
                      surface.instanceDisplayName || surface.instanceId || "未接管",
                    ].join(" · ")}
                  </p>
                  <div className="definition-list">
                    <div>
                      <dt>当前线程</dt>
                      <dd>{surface.threadTitle || surface.nextThreadTitle || "未选择"}</dd>
                    </div>
                    <div>
                      <dt>队列</dt>
                      <dd>{surface.queuedCount}</dd>
                    </div>
                    <div>
                      <dt>Pending Request</dt>
                      <dd>{surface.pendingRequestCount}</dd>
                    </div>
                    <div>
                      <dt>最近活跃</dt>
                      <dd>{surface.lastActiveAt ? formatTimestamp(surface.lastActiveAt) : "无"}</dd>
                    </div>
                  </div>
                  {surface.pendingRequest ? (
                    <div className="admin-subpanel" style={{ marginTop: "0.75rem" }}>
                      <div className="inline-status-row">
                        <strong>{surface.pendingRequest.title || "待处理请求"}</strong>
                        <span className={`status-badge ${surface.pendingRequest.needsRedelivery ? "warn" : "neutral"}`}>
                          {[
                            surface.pendingRequest.requestType || "request",
                            surface.pendingRequest.lifecycleState || "",
                          ]
                            .filter(Boolean)
                            .join(" · ")}
                        </span>
                      </div>
                      <p className="support-copy">
                        {formatPendingRequestProgress(surface.pendingRequest)}
                      </p>
                      {surface.pendingRequest.lastDeliveryError ? (
                        <p className="support-copy">
                          投递异常：{surface.pendingRequest.lastDeliveryError}
                        </p>
                      ) : null}
                    </div>
                  ) : null}
                  {surface.lastDeliveryError ? (
                    <div className="notice-banner warn">
                      最近失败：{surface.lastDeliveryError}
                    </div>
                  ) : null}
                  {surface.peerSurfaces && surface.peerSurfaces.length > 0 ? (
                    <div className="detail-stack" style={{ marginTop: "0.75rem" }}>
                      <strong>同实例其他入口</strong>
                      {surface.peerSurfaces.map((peer) => (
                        <div key={peer.surfaceSessionId} className="admin-subpanel">
                          <div className="inline-status-row">
                            <strong>{peer.platform || peer.gatewayId || peer.surfaceSessionId}</strong>
                            <span className={`status-badge ${peerTone(peer)}`}>
                              {peer.sharedAttach ? "shared" : "owner"}
                            </span>
                          </div>
                          <p className="support-copy">
                            {[
                              peer.activeRemoteTurn ? "active turn" : peer.pendingRemoteTurn ? "dispatching" : peer.activeItemStatus || "idle",
                              peer.queuedCount > 0 ? `${peer.queuedCount} queued` : "",
                              peer.hasPendingRequest ? `${peer.pendingRequestCount} requests` : "",
                            ]
                              .filter(Boolean)
                              .join(" · ")}
                          </p>
                        </div>
                      ))}
                    </div>
                  ) : null}
                </article>
              ))}
            </div>
          )}
        </div>
      </section>
    );
  }

  function renderAutoConfigCard() {
    if (!selectedApp) {
      return null;
    }
    const disabled =
      Boolean(selectedApp.runtimeApply?.pending) ||
      actionBusy === "auto-config-apply" ||
      actionBusy === "auto-config-publish";
    const authLink = selectedApp.consoleLinks?.auth?.trim();
    const botLink = selectedApp.consoleLinks?.bot?.trim();

    if (selectedAutoConfig.status === "idle" || selectedAutoConfig.status === "loading") {
      return (
        <article className="soft-card-v2" style={{ marginTop: "1rem" }}>
          <h4>飞书自动配置</h4>
          <div className="notice-banner warn">正在检查当前配置，请稍候...</div>
        </article>
      );
    }

    if (selectedAutoConfig.status === "error") {
      return (
        <article className="soft-card-v2" style={{ marginTop: "1rem" }}>
          <h4>飞书自动配置</h4>
          <div className="detail-stack">
            <div className="notice-banner warn">{selectedAutoConfig.message}</div>
            <div className="button-row">
              <button
                className="secondary-button"
                type="button"
                disabled={disabled}
                onClick={() => void loadAutoConfigPlan(selectedApp.id)}
              >
                重新检查
              </button>
            </div>
          </div>
        </article>
      );
    }

    const plan = selectedAutoConfig.data.plan;
    const blockingRequirements = (plan.blockingRequirements || []).filter(
      (item) => !item.present,
    );
    const degradableRequirements = (plan.degradableRequirements || []).filter(
      (item) => !item.present,
    );

    return (
      <article className="soft-card-v2" style={{ marginTop: "1rem" }}>
        <div className="detail-stack">
          <div>
            <h4>飞书自动配置</h4>
            <p>{plan.summary?.trim() || describeAutoConfigSummary(plan.status)}</p>
          </div>
          <div className={`notice-banner ${autoConfigNoticeTone(plan.status)}`}>
            {describeAutoConfigHeadline(plan.status)}
          </div>
          {plan.blockingReason ? (
            <p className="support-copy">
              当前原因：{describeAutoConfigBlockingReason(plan.blockingReason)}
            </p>
          ) : null}
          {renderRequirementSection("还需要处理", blockingRequirements)}
          {renderRequirementSection("可按降级继续", degradableRequirements)}
          <div className="button-row">
            {plan.status === "apply_required" ? (
              <button
                className="primary-button"
                type="button"
                disabled={disabled}
                onClick={() => void applyAutoConfig()}
              >
                自动补齐配置
              </button>
            ) : null}
            {plan.status === "publish_required" ? (
              <button
                className="primary-button"
                type="button"
                disabled={disabled}
                onClick={() => setPublishTargetID(selectedApp.id)}
              >
                提交发布
              </button>
            ) : null}
            <button
              className="secondary-button"
              type="button"
              disabled={disabled}
              onClick={() => void loadAutoConfigPlan(selectedApp.id)}
            >
              重新检查
            </button>
          </div>
          {authLink || botLink ? (
            <p className="support-copy">
              {authLink ? (
                <>
                  如需在飞书后台继续查看权限或发布状态，请前往{" "}
                  <a
                    className="inline-link"
                    href={authLink}
                    rel="noreferrer"
                    target="_blank"
                  >
                    应用权限页面
                  </a>
                  。
                </>
              ) : null}
              {authLink && botLink ? <br /> : null}
              {botLink ? (
                <>
                  机器人菜单仍需手动确认，可继续打开{" "}
                  <a
                    className="inline-link"
                    href={botLink}
                    rel="noreferrer"
                    target="_blank"
                  >
                    机器人后台
                  </a>
                  。
                </>
              ) : null}
            </p>
          ) : null}
        </div>
      </article>
    );
  }

  function renderRobotDetail() {
    if (!selectedApp) {
      return (
        <section className="panel">
          <div className="step-stage-head">
            <h2>新增机器人</h2>
            <p>扫码或手动输入接入飞书应用</p>
          </div>
          <div className="choice-toggle">
            <button
              className={connectMode === "qr" ? "primary-button" : "ghost-button"}
              type="button"
              onClick={() => changeConnectMode("qr")}
            >
              扫码创建
            </button>
            <button
              className={
                connectMode === "manual" ? "primary-button" : "ghost-button"
              }
              type="button"
              onClick={() => changeConnectMode("manual")}
            >
              手动输入
            </button>
          </div>
          {connectMode === "qr" ? (
            <div className="panel">
              <div className="scan-preview">
                <div>
                  <h4 style={{ margin: 0 }}>扫码创建</h4>
                  <p className="support-copy">
                    使用飞书扫描二维码，页面将自动完成后续操作
                  </p>
                  <div className="scan-frame">
                    {onboardingSession?.qrCodeDataUrl ? (
                      <img alt="飞书扫码创建二维码" src={onboardingSession.qrCodeDataUrl} />
                    ) : (
                      <span>二维码准备中</span>
                    )}
                  </div>
                </div>
                <div className="detail-stack">
                  {onboardingSession?.status === "pending" ? (
                    <div className="notice-banner warn">正在等待扫码结果...</div>
                  ) : null}
                  {onboardingSession?.status === "ready" && !connectError ? (
                    <div className="notice-banner good">
                      扫码成功，连接验证已通过，正在加入机器人列表...
                    </div>
                  ) : null}
                  {onboardingSession?.status === "failed" ||
                  onboardingSession?.status === "expired" ||
                  connectError ? (
                    <div className="notice-banner danger">
                      {connectError || "当前扫码没有继续成功，请重新开始。"}
                    </div>
                  ) : null}
                  <div className="button-row">
                    {(connectError ||
                      onboardingSession?.status === "failed" ||
                      onboardingSession?.status === "expired") && (
                      <button
                        className="secondary-button"
                        type="button"
                        disabled={actionBusy === "qr-start"}
                        onClick={() => resetConnectFlow()}
                      >
                        重新扫码
                      </button>
                    )}
                    {onboardingSession?.status === "ready" && connectError ? (
                      <button
                        className="secondary-button"
                        type="button"
                        disabled={actionBusy === "qr-complete"}
                        onClick={() => {
                          if (onboardingSession?.id) {
                            clearConnectError();
                            void completeQRCodeSession(onboardingSession.id);
                          }
                        }}
                      >
                        重新验证
                      </button>
                    ) : null}
                    <button
                      className="ghost-button"
                      type="button"
                      onClick={() => changeConnectMode("manual")}
                    >
                      改用手动输入
                    </button>
                  </div>
                </div>
              </div>
            </div>
          ) : (
            <div className="panel">
              <div className="form-grid">
                <label className="field">
                  <span>
                    App ID <em className="field-required">*</em>
                  </span>
                  <input
                    aria-label="App ID"
                    placeholder="请输入 App ID"
                    value={newRobotForm.appId}
                    onChange={(event) =>
                      setNewRobotForm((current) => ({
                        ...current,
                        appId: event.target.value,
                      }))
                    }
                  />
                </label>
                <label className="field">
                  <span>
                    App Secret <em className="field-required">*</em>
                  </span>
                  <input
                    aria-label="App Secret"
                    placeholder="请输入 App Secret"
                    value={newRobotForm.appSecret}
                    onChange={(event) =>
                      setNewRobotForm((current) => ({
                        ...current,
                        appSecret: event.target.value,
                      }))
                    }
                  />
                </label>
                <label className="field form-grid-span-2">
                  <span>机器人名称（可选）</span>
                  <input
                    aria-label="机器人名称（可选）"
                    placeholder="例如：运营机器人"
                    value={newRobotForm.name}
                    onChange={(event) =>
                      setNewRobotForm((current) => ({
                        ...current,
                        name: event.target.value,
                      }))
                    }
                  />
                </label>
              </div>
              <div className="button-row">
                <button
                  className="primary-button"
                  type="button"
                  disabled={actionBusy === "create-robot"}
                  onClick={() => void createRobot()}
                >
                  连接并验证
                </button>
              </div>
            </div>
          )}
          {detailNotice ? (
            <div className={`notice-banner ${detailNotice.tone}`}>
              {detailNotice.message}
            </div>
          ) : null}
        </section>
      );
    }

    return (
      <section className="panel">
        <div className="step-stage-head">
          <h2>{selectedApp.name || "未命名机器人"}</h2>
          <p>连接状态与自动配置</p>
        </div>
        <dl className="definition-list">
          <div>
            <dt>App ID</dt>
            <dd>{selectedApp.appId || "未填写"}</dd>
          </div>
          <div>
            <dt>连接</dt>
            <dd>{describeConnectionState(selectedApp)}</dd>
          </div>
          <div>
            <dt>启用状态</dt>
            <dd>{selectedApp.enabled ? "已启用" : "未启用"}</dd>
          </div>
          <div>
            <dt>最近验证</dt>
            <dd>{selectedApp.verifiedAt ? formatTimestamp(selectedApp.verifiedAt) : "暂未验证"}</dd>
          </div>
        </dl>
        {selectedApp.runtimeApply?.pending ? (
          <div className="notice-banner warn">
            当前机器人还在同步设置，请稍后刷新状态后再继续操作。
          </div>
        ) : null}
        {renderAutoConfigCard()}
        {detailNotice ? (
          <div className={`notice-banner ${detailNotice.tone}`}>
            {detailNotice.message}
          </div>
        ) : null}
        <div className="button-row">
          <button
            className="danger-button"
            type="button"
            disabled={Boolean(selectedApp.readOnly)}
            onClick={() => setDeleteTargetID(selectedApp.id)}
          >
            删除机器人
          </button>
        </div>
        {selectedApp.readOnly ? (
          <p className="support-copy">当前机器人由运行环境提供，不能在这里删除。</p>
        ) : null}
      </section>
    );
  }

  if (loading) {
    return (
      <div className="product-page">
        <header className="product-topbar">
          <h1>{versionTitle}</h1>
          <p>飞书机器人、系统集成与本地存储</p>
        </header>
        <section className="panel">
          <div className="empty-state">
            <div className="loading-dot" />
            <span>正在读取最新状态</span>
          </div>
        </section>
      </div>
    );
  }

  if (loadError) {
    return (
      <div className="product-page">
        <header className="product-topbar">
          <h1>{versionTitle}</h1>
          <p>飞书机器人、系统集成与本地存储</p>
        </header>
        <section className="panel">
          <div className="empty-state error">
            <strong>当前页面暂时无法打开</strong>
            <p>{loadError}</p>
            <div className="button-row">
              <button
                className="secondary-button"
                type="button"
                onClick={() => void loadAdminPage()}
              >
                重新加载
              </button>
            </div>
          </div>
        </section>
      </div>
    );
  }

  return (
    <div className="product-page">
      <header className="product-topbar">
        <h1>{versionTitle}</h1>
        <p>飞书机器人、系统集成与本地存储</p>
      </header>

      {renderRuntimePanel()}

      <section className="panel">
        <div className="step-stage-head">
          <h2>机器人管理</h2>
          <p>已接入的飞书应用</p>
        </div>
        <div className="robot-layout" style={{ marginTop: "1rem" }}>
          <div className="robot-list">
            {apps.map((app) => {
              const autoConfigState = autoConfigPlans[app.id];
              let planStatus = "";
              if (app.runtimeApply?.pending) {
                planStatus = "runtime_pending";
              } else if (autoConfigState?.status === "ready") {
                planStatus = autoConfigState.data.plan.status;
              } else if (autoConfigState?.status === "loading") {
                planStatus = "loading";
              }
              const statusTag = describeAutoConfigTag(planStatus);
              return (
                <button
                  key={app.id}
                  className={`robot-list-button${selectedRobotID === app.id ? " active" : ""}`}
                  type="button"
                  onClick={() => {
                    setDetailNotice(null);
                    setSelectedRobotID(app.id);
                  }}
                >
                  <div className="robot-list-head">
                    <strong>{app.name || "未命名机器人"}</strong>
                    {statusTag ? (
                      <span className={`robot-tag${statusTag.warn ? " warn" : ""}`}>
                        {statusTag.label}
                      </span>
                    ) : null}
                  </div>
                  <p>{app.appId || "未填写 App ID"}</p>
                </button>
              );
            })}
            <button
              className={`robot-list-button${selectedRobotID === newRobotID ? " active" : ""}`}
              type="button"
              onClick={() => {
                setDetailNotice(null);
                setSelectedRobotID(newRobotID);
              }}
            >
              <div className="robot-list-head">
                <strong>新增机器人</strong>
                <span className="robot-tag">新增</span>
              </div>
              <p>点击开始接入</p>
            </button>
          </div>
          {renderRobotDetail()}
        </div>
      </section>

      <section className="panel">
        <div className="step-stage-head">
          <h2>企微机器人</h2>
          <p>多账号管理、重连与运行态联动</p>
        </div>
        {wecomBotsError ? <div className="notice-banner warn">{wecomBotsError}</div> : null}
        <div className="soft-grid two-column" style={{ marginTop: "1rem" }}>
          <article className="soft-card-v2">
            <h4>{editingWeComBotID ? "编辑企微机器人" : "新增企微机器人"}</h4>
            <div className="form-grid">
              <label className="field">
                <span>标识 ID（可选）</span>
                <input
                  aria-label="企微标识 ID"
                  placeholder="例如 ops"
                  value={wecomBotForm.id}
                  onChange={(event) =>
                    setWeComBotForm((current) => ({ ...current, id: event.target.value }))
                  }
                />
              </label>
              <label className="field">
                <span>企微别名（可选）</span>
                <input
                  aria-label="企微别名"
                  placeholder="例如 Ops Bot"
                  value={wecomBotForm.name}
                  onChange={(event) =>
                    setWeComBotForm((current) => ({ ...current, name: event.target.value }))
                  }
                />
              </label>
              <label className="field">
                <span>
                  Bot ID <em className="field-required">*</em>
                </span>
                <input
                  aria-label="企微 Bot ID"
                  placeholder="请输入企微 Bot ID"
                  value={wecomBotForm.botId}
                  onChange={(event) =>
                    setWeComBotForm((current) => ({ ...current, botId: event.target.value }))
                  }
                />
              </label>
              <label className="field">
                <span>
                  Secret {!editingWeComBotID ? <em className="field-required">*</em> : null}
                </span>
                <input
                  aria-label="企微 Secret"
                  placeholder={editingWeComBotID ? "留空则保持原 Secret" : "请输入企微 Secret"}
                  value={wecomBotForm.secret}
                  onChange={(event) =>
                    setWeComBotForm((current) => ({ ...current, secret: event.target.value }))
                  }
                />
              </label>
              <label className="field">
                <span>运行状态</span>
                <input
                  aria-label="企微启用状态"
                  type="checkbox"
                  checked={wecomBotForm.enabled}
                  onChange={(event) =>
                    setWeComBotForm((current) => ({ ...current, enabled: event.target.checked }))
                  }
                />
              </label>
            </div>
            {editingWeComBotID ? (
              <p className="support-copy">编辑时 Secret 留空会保留当前值。</p>
            ) : null}
            <div className="button-row">
              <button
                className="primary-button"
                type="button"
                disabled={
                  actionBusy === "create-wecom-bot" ||
                  actionBusy === `update-wecom-${editingWeComBotID || ""}`
                }
                onClick={() => void saveWeComBot()}
              >
                {editingWeComBotID ? "保存企微机器人" : "新增企微机器人"}
              </button>
              {editingWeComBotID ? (
                <button
                  className="ghost-button"
                  type="button"
                  disabled={actionBusy === `update-wecom-${editingWeComBotID}`}
                  onClick={() => resetWeComBotForm()}
                >
                  取消编辑
                </button>
              ) : null}
            </div>
          </article>
          <article className="soft-card-v2">
            <h4>已配置账号</h4>
            {wecomBots.length === 0 ? (
              <p>当前没有已配置的企微机器人。</p>
            ) : (
              <div className="detail-stack">
                {wecomBots.map((bot) => (
                  <div key={bot.id} className="admin-subpanel">
                    <div className="inline-status-row">
                      <strong>{bot.name || bot.id}</strong>
                      <span className={`status-badge ${wecomStateTone(bot.runtime)}`}>
                        {wecomStateLabel(bot.runtime)}
                      </span>
                    </div>
                    <p className="support-copy">
                      {[bot.id, bot.botId || "", bot.persisted ? "persisted" : "runtime"]
                        .filter(Boolean)
                        .join(" · ")}
                    </p>
                    <p className="support-copy">
                      {bot.runtime?.lastError?.trim()
                        ? `最近失败：${bot.runtime.lastError}`
                        : bot.runtime?.lastConnectedAt
                          ? `最近连通：${formatTimestamp(bot.runtime.lastConnectedAt)}`
                          : "还没有连通记录"}
                    </p>
                    <div className="button-row">
                      <button
                        className="secondary-button"
                        type="button"
                        disabled={!bot.persisted}
                        onClick={() => startEditWeComBot(bot)}
                      >
                        编辑
                      </button>
                      <button
                        className="secondary-button"
                        type="button"
                        disabled={actionBusy === `reconnect-wecom-${bot.id}`}
                        onClick={() => void reconnectWeComBot(bot.id)}
                      >
                        重连
                      </button>
                      <button
                        className="danger-button"
                        type="button"
                        disabled={actionBusy === `delete-wecom-${bot.id}` || !bot.persisted}
                        onClick={() => void deleteWeComBot(bot.id)}
                      >
                        删除
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </article>
        </div>
      </section>

      <section className="panel">
        <div className="step-stage-head">
          <h2>系统集成</h2>
          <p>自动运行与 VS Code 集成</p>
        </div>
        <div className="soft-grid two-column" style={{ marginTop: "1rem" }}>
          <article className="soft-card-v2">
            <h4>自动运行设置</h4>
            <p>{describeAutostart(autostart, autostartError)}</p>
            {autostartError ? (
              <div className="notice-banner warn">{autostartError}</div>
            ) : null}
            {!autostartError && autostart?.supported && !autostart.enabled ? (
              <div className="button-row">
                <button
                  className="secondary-button"
                  type="button"
                  disabled={actionBusy === "autostart" || !autostart.canApply}
                  onClick={() => void enableAutostart()}
                >
                  启用自动运行
                </button>
              </div>
            ) : null}
          </article>
          <article className="soft-card-v2">
            <h4>VS Code 集成</h4>
            <p>{describeVSCode(vscode, vscodeError)}</p>
            {vscodeError ? (
              <div className="notice-banner warn">{vscodeError}</div>
            ) : null}
            <div className="button-row">
              <button
                className="ghost-button"
                type="button"
                disabled={actionBusy === "vscode"}
                onClick={() => void repairVSCode()}
              >
                重新检查并修复
              </button>
            </div>
          </article>
        </div>
      </section>

      <ClaudeProfileSection
        loadError={claudeProfilesError}
        profiles={claudeProfiles}
        setProfiles={setClaudeProfiles}
        onReload={async () => {
          await loadAdminPage({ preferredRobotID: selectedRobotID });
        }}
      />

      <CodexProviderSection
        loadError={codexProvidersError}
        providers={codexProviders}
        setProviders={setCodexProviders}
        onReload={async () => {
          await loadAdminPage({ preferredRobotID: selectedRobotID });
        }}
      />

      <section className="panel">
        <div className="step-stage-head">
          <h2>存储管理</h2>
          <p>预览文件、图片暂存与日志清理</p>
        </div>
        <div className="soft-grid" style={{ marginTop: "1rem" }}>
          <article className="soft-card-v2">
            <h4>预览文件</h4>
            <p>
              {formatFileSummary(previewSummary.fileCount, previewSummary.bytes)}
            </p>
            {previewError ? <div className="notice-banner warn">{previewError}</div> : null}
            <div className="button-row">
              <button
                className="secondary-button"
                type="button"
                disabled={actionBusy === "cleanup-preview" || apps.length === 0}
                onClick={() => void cleanupPreviewDrive()}
              >
                清理旧预览
              </button>
            </div>
          </article>
          <article className="soft-card-v2">
            <h4>图片暂存</h4>
            <p>
              {formatFileSummary(
                imageStaging?.fileCount || 0,
                imageStaging?.totalBytes || 0,
              )}
            </p>
            {imageStagingError ? (
              <div className="notice-banner warn">{imageStagingError}</div>
            ) : null}
            <div className="button-row">
              <button
                className="secondary-button"
                type="button"
                disabled={actionBusy === "cleanup-image"}
                onClick={() => void cleanupImageStaging()}
              >
                清理旧图片
              </button>
            </div>
          </article>
          <article className="soft-card-v2">
            <h4>日志文件</h4>
            <p>
              {formatFileSummary(
                logsStorage?.fileCount || 0,
                logsStorage?.totalBytes || 0,
              )}
            </p>
            {logsStorageError ? (
              <div className="notice-banner warn">{logsStorageError}</div>
            ) : null}
            <div className="button-row">
              <button
                className="secondary-button"
                type="button"
                disabled={actionBusy === "cleanup-logs"}
                onClick={() => void cleanupLogsStorage()}
              >
                清理一天前日志
              </button>
            </div>
          </article>
        </div>
      </section>

      {publishTargetID ? (
        <div className="modal-backdrop" role="presentation">
          <div
            className="modal-card"
            role="dialog"
            aria-modal="true"
            aria-labelledby="publish-app-title"
          >
            <h3 id="publish-app-title">确认提交发布</h3>
            <p className="modal-copy">
              这会把当前自动补齐后的飞书配置提交到发布流程。若飞书要求管理员审核，后续状态会显示为“等待管理员处理”。
            </p>
            <div className="modal-actions">
              <button
                className="ghost-button"
                type="button"
                onClick={() => setPublishTargetID(null)}
              >
                取消
              </button>
              <button
                className="primary-button"
                type="button"
                disabled={actionBusy === "auto-config-publish"}
                onClick={() => void publishAutoConfig()}
              >
                确认提交
              </button>
            </div>
          </div>
        </div>
      ) : null}

      {deleteTargetID ? (
        <div className="modal-backdrop" role="presentation">
          <div
            className="modal-card"
            role="dialog"
            aria-modal="true"
            aria-labelledby="delete-robot-title"
          >
            <h3 id="delete-robot-title">确认删除机器人</h3>
            <p className="modal-copy">
              删除后将移除“
              {apps.find((app) => app.id === deleteTargetID)?.name || "当前机器人"}
              ”，此操作不可恢复。
            </p>
            <div className="modal-actions">
              <button
                className="ghost-button"
                type="button"
                onClick={() => setDeleteTargetID(null)}
              >
                取消
              </button>
              <button
                className="danger-button"
                type="button"
                disabled={actionBusy === "delete-robot"}
                onClick={() => void deleteRobot()}
              >
                确认删除
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

async function safeRequest<T>(path: string) {
  try {
    return {
      data: await requestJSON<T>(path),
      error: "",
    };
  } catch {
    return {
      data: null,
      error: "暂时没有读取成功，请稍后重试。",
    };
  }
}

function buildAdminPageTitle(bootstrap: BootstrapState | null): string {
  const name = bootstrap?.product.name?.trim() || "Codex Remote Feishu";
  const version = bootstrap?.product.version?.trim();
  return version ? `${name} ${version} 管理` : `${name} 管理`;
}

function describeConnectionState(app: FeishuAppSummary): string {
  switch (app.status?.state) {
    case "connected":
      return "连接正常";
    case "disabled":
      return "已停用";
    case "error":
      return "需要处理";
    default:
      return "待确认";
  }
}

function gatewayStateTone(state: string | undefined, disabled: boolean | undefined) {
  if (disabled || state === "disabled") {
    return "neutral";
  }
  if (state === "connected") {
    return "good";
  }
  return "warn";
}

function gatewayStateLabel(state: string | undefined, disabled: boolean | undefined): string {
  if (disabled || state === "disabled") {
    return "disabled";
  }
  if (state === "connected") {
    return "connected";
  }
  return state?.trim() || "unknown";
}

function wecomStateTone(wecom: RuntimeWeComStatus | null | undefined) {
  if (!wecom || !wecom.enabled) {
    return "neutral";
  }
  if (wecom.connected || wecom.state === "connected") {
    return "good";
  }
  if (wecom.state === "connecting") {
    return "neutral";
  }
  return "warn";
}

function wecomStateLabel(wecom: RuntimeWeComStatus | null | undefined): string {
  if (!wecom || !wecom.enabled) {
    return "disabled";
  }
  return wecom.state?.trim() || (wecom.connected ? "connected" : "unknown");
}

function surfaceTone(surface: RuntimeSurfaceStatus | undefined) {
  if (!surface) {
    return "neutral";
  }
  if (surface.lastDeliveryError || surface.needsRedelivery) {
    return "warn";
  }
  if (surface.activeRemoteTurn || surface.pendingRemoteTurn || surface.queuedCount > 0) {
    return "good";
  }
  return "neutral";
}

function surfaceBadgeLabel(surface: RuntimeSurfaceStatus | undefined): string {
  if (!surface) {
    return "idle";
  }
  if (surface.lastDeliveryError || surface.needsRedelivery) {
    return "delivery issue";
  }
  if (surface.activeRemoteTurn) {
    return "active turn";
  }
  if (surface.pendingRemoteTurn) {
    return "dispatching";
  }
  if (surface.hasPendingRequest) {
    return "pending request";
  }
  if (surface.queuedCount > 0) {
    return `${surface.queuedCount} queued`;
  }
  return "idle";
}

function peerTone(peer: RuntimePeerSurfaceStatus | undefined) {
  if (!peer) {
    return "neutral";
  }
  if (peer.activeRemoteTurn) {
    return "good";
  }
  if (peer.pendingRemoteTurn || peer.hasPendingRequest || peer.queuedCount > 0) {
    return "warn";
  }
  return "neutral";
}

function describeAutostart(
  autostart: AutostartDetectResponse | null,
  error: string,
): string {
  if (error) {
    return "暂时没有读取成功。";
  }
  if (!autostart) {
    return "暂时没有读取成功。";
  }
  if (!autostart.supported) {
    return "当前系统不支持。";
  }
  return autostart.enabled ? "当前已启用。" : "当前未启用。";
}

function formatPercent(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "0%";
  }
  return `${(value * 100).toFixed(value >= 0.995 ? 0 : 1)}%`;
}

function describeVSCode(
  vscode: VSCodeDetectResponse | null,
  error: string,
): string {
  if (error) {
    return "暂时没有读取成功。";
  }
  if (!vscode) {
    return "暂时没有读取成功。";
  }
  return vscodeIsReady(vscode)
    ? "当前已接入。"
    : "检测到 VS Code 集成未完成，请先修复。";
}

function formatBytes(value: number): string {
  if (value <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let current = value;
  let index = 0;
  while (current >= 1024 && index < units.length - 1) {
    current /= 1024;
    index += 1;
  }
  return `${current >= 100 || index === 0 ? current.toFixed(0) : current.toFixed(1)} ${units[index]}`;
}

function formatFileSummary(fileCount: number, bytes: number): string {
  return `${fileCount} 个文件，约 ${formatBytes(bytes)}`;
}

function formatPendingRequestProgress(request: {
  currentQuestionIndex?: number;
  questionCount?: number;
  answeredCount?: number;
  skippedCount?: number;
  pendingDispatch?: boolean;
  visible?: boolean;
}) {
  const parts: string[] = [];
  if ((request.questionCount || 0) > 0) {
    parts.push(`第 ${(request.currentQuestionIndex || 0) + 1} / ${request.questionCount} 题`);
  }
  if ((request.answeredCount || 0) > 0 || (request.skippedCount || 0) > 0) {
    parts.push(`已答 ${request.answeredCount || 0}`);
    if ((request.skippedCount || 0) > 0) {
      parts.push(`跳过 ${request.skippedCount || 0}`);
    }
  }
  if (request.pendingDispatch) {
    parts.push("等待回写");
  } else if (request.visible) {
    parts.push("已投影到当前入口");
  }
  return parts.join(" · ") || "待处理请求";
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "暂不可用";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}
