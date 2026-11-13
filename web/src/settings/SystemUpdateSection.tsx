import {
  ArrowDownToLine,
  CircleAlert,
  CircleCheck,
  CircleFadingArrowUp,
  CircleX,
  LoaderCircle,
  type LucideIcon,
  PackageCheck,
  RotateCcw,
  TriangleAlert,
} from "lucide-react";
import { Switch } from "radix-ui";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { formatBytes } from "../lib/bytes";
import { calendarMoment, relativeTime } from "../lib/time";
import {
  requestSystemUpdate,
  type SystemUpdateAction,
  type SystemUpdateRecord,
  type SystemUpdateSettings,
  type SystemUpdateStatus,
  updateSystemUpdateSettings,
} from "../systemupdate/api";
import { publishSystemUpdate, reloadSystemUpdate, updateBusy, useSystemUpdate } from "../systemupdate/status";
import { byText, problemText, versionLabel, versionParts, waitingText } from "../systemupdate/texts";
import { Banner, Group, Modal, PageHeader, Pill, Row } from "./parts";
import styles from "./settings.module.css";

type Action = SystemUpdateAction["action"];

// The page checks again when it opens and the last check is older than this.
const checkAfterMs = 60 * 60_000;

const suiteLabels: Record<string, string> = { release: "正式版", dev: "开发版" };

// SystemUpdateSection is Settings → 系统更新, for administrators
// (docs/specs/system-update.md): the state of the update with its one next
// step, the versions, the automatic download and install, and what happened.
export function SystemUpdateSection({ onOpenLogs }: { onOpenLogs: () => void }) {
  const { status, error: readError, pageVersion } = useSystemUpdate(true);
  const [sending, setSending] = useState<Action>();
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<"install" | "revert">();

  // Opening the page checks when the last check is an hour old.
  const looked = useRef(false);
  useEffect(() => {
    if (!status || looked.current) return;
    looked.current = true;
    const stale = !status.checkedAt || Date.now() - Date.parse(status.checkedAt) >= checkAfterMs;
    if (stale && !updateBusy(status) && status.problem?.code !== "not_installed")
      requestSystemUpdate("check").then(publishSystemUpdate, () => undefined);
  }, [status]);

  const act = async (action: Action) => {
    setSending(action);
    setError("");
    try {
      publishSystemUpdate(await requestSystemUpdate(action));
      return true;
    } catch (caught) {
      setError(errorText(caught, "systemUpdate"));
      reloadSystemUpdate();
      return false;
    } finally {
      setSending(undefined);
    }
  };

  const header = <PageHeader title="系统更新" />;
  if (!status)
    return (
      <>
        {header}
        <Group>
          {readError ? (
            <Row label="无法读取更新状态" detail={errorText(readError, "systemUpdate")}>
              <button className={styles.setBtn} onClick={reloadSystemUpdate}>
                重试
              </button>
            </Row>
          ) : (
            <Row label="正在读取…" />
          )}
        </Group>
      </>
    );

  const busy = updateBusy(status) || sending !== undefined;
  const changed = pageVersion && status.current && status.current !== pageVersion && !updateBusy(status);
  return (
    <>
      {header}
      {changed && (
        <div className={`${styles.setBanner} ${styles.success} ${styles.updReload}`} role="status">
          <CircleCheck />
          <span>现在运行的是 A-NAS {versionLabel(status.current ?? "")}，刷新页面后使用它的界面</span>
          <button className={`${styles.setBtn} primary`} onClick={() => window.location.reload()}>
            刷新页面
          </button>
        </div>
      )}
      {error && (
        <Banner tone="error" onClose={() => setError("")}>
          {error}
        </Banner>
      )}
      <StatusCard
        status={status}
        restarting={readError !== undefined && updateBusy(status)}
        sending={sending}
        onAct={(action) => (action === "install" ? setConfirm("install") : void act(action))}
      />
      <Group title="版本" anchor="update-version">
        <Row
          label="当前版本"
          detail={status.installedAt ? `安装于 ${calendarMoment(status.installedAt)}` : undefined}
          value={<VersionValue version={status.current} />}
        />
        {status.candidate && status.phase !== "up-to-date" && (
          <Row
            label="新版本"
            detail={status.downloadBytes > 0 ? `下载 ${formatBytes(status.downloadBytes)}` : undefined}
            value={<VersionValue version={status.candidate} />}
          />
        )}
        {status.suites.length > 0 && (
          <Row label="更新渠道" value={status.suites.map((suite) => suiteLabels[suite] ?? suite).join("与")} />
        )}
      </Group>
      <SettingsGroup settings={status.settings} onError={setError} />
      <Group
        title="记录"
        anchor="update-records"
        aside={
          <button className={styles.setLink} onClick={onOpenLogs}>
            在日志中查看
          </button>
        }
      >
        {status.records.length === 0 && <Row label="还没有记录" />}
        {status.records.map((record) => (
          <RecordRow key={`${record.at} ${record.kind}`} record={record} />
        ))}
      </Group>
      {status.previous && (
        <Group anchor="update-revert">
          <button
            className={`${styles.setRow} ${styles.setRowCommand}`}
            disabled={busy}
            onClick={() => setConfirm("revert")}
          >
            回到上一版本 {versionLabel(status.previous)}
          </button>
        </Group>
      )}
      {confirm && (
        <ConfirmDialog
          kind={confirm}
          status={status}
          onClose={() => setConfirm(undefined)}
          onConfirm={async () => {
            // A refusal shows on the page, as the dialog closes.
            await act(confirm);
            setConfirm(undefined);
          }}
        />
      )}
    </>
  );
}

interface Presentation {
  tone: "success" | "accent" | "warning" | "danger";
  icon: LucideIcon;
  spin?: boolean;
  title: string;
  detail?: string;
  // note is a problem beside a state that is otherwise fine.
  note?: string;
  // progress is a share from 0 to 1, or null when it cannot be measured.
  progress?: number | null;
  action?: { action: Action; label: string };
  // why is what apt or a-nas-check said.
  why?: string;
}

function present(status: SystemUpdateStatus, restarting: boolean): Presentation {
  const { problem, settings } = status;
  const candidate = versionLabel(status.candidate ?? "");
  const why = problem?.detail && problem.code !== "disk_space" ? problem.detail : undefined;
  const note = problem ? problemText(problem) : undefined;
  const check = { action: "check" as const, label: "检查更新" };
  const restart = restarting ? "服务正在重启，恢复后自动刷新" : "安装时服务会重启，恢复后自动刷新";
  switch (status.phase) {
    case "available":
      return {
        tone: "accent",
        icon: CircleFadingArrowUp,
        title: `有新版本 ${candidate}`,
        detail: status.downloadBytes > 0 ? `需要下载 ${formatBytes(status.downloadBytes)}` : undefined,
        note,
        why,
        action: { action: "download", label: "下载" },
      };
    case "downloading": {
      const { downloadBytes: total, downloadedBytes: done } = status;
      return {
        tone: "accent",
        icon: ArrowDownToLine,
        title: `正在下载 ${candidate}`,
        detail: total > 0 ? `${formatBytes(done)} / ${formatBytes(total)}` : undefined,
        progress: total > 0 ? Math.min(done / total, 1) : null,
      };
    }
    case "ready":
      return {
        tone: "accent",
        icon: CircleFadingArrowUp,
        title: `${candidate} 可以安装`,
        detail:
          status.waiting.length > 0
            ? `${status.waiting.map((what) => waitingText[what] ?? what).join("，")}，结束后可以安装`
            : settings.autoInstall
              ? `将在 ${settings.windowStart}–${settings.windowEnd} 之间自动安装`
              : undefined,
        note,
        why,
        action: { action: "install", label: "安装" },
      };
    case "installing":
      return {
        tone: "accent",
        icon: LoaderCircle,
        spin: true,
        title: `正在安装 ${versionLabel(status.target ?? "")}`,
        detail: restart,
        progress: null,
      };
    case "rolling-back":
      return {
        tone: "accent",
        icon: LoaderCircle,
        spin: true,
        title: `正在回到 ${versionLabel(status.target ?? "")}`,
        detail: restart,
        progress: null,
      };
    case "rolled-back": {
      // The rollback's record; failed checks since then may be newer.
      const record = status.records.find((record) => record.kind === "rolled-back" || record.kind === "failed");
      if (problem?.code === "rollback")
        return {
          tone: "danger",
          icon: CircleX,
          title: "回到原版本时出错",
          detail: "A-NAS 可能无法正常运行，请按部署手册的“回滚或恢复”处理",
          why,
        };
      return {
        tone: "warning",
        icon: TriangleAlert,
        title: record?.version
          ? `${versionLabel(record.version)} 安装后检查没有通过，已回到 ${versionLabel(status.current ?? "")}`
          : `已回到 ${versionLabel(status.current ?? "")}`,
        detail: note,
        why,
        action: check,
      };
    }
    case "check-failed":
      return {
        tone: "warning",
        icon: CircleAlert,
        title: "无法检查更新",
        detail: note,
        why,
        action: problem?.code === "not_installed" ? undefined : { action: "check", label: "重试" },
      };
    case "up-to-date":
      return {
        tone: "success",
        icon: PackageCheck,
        title: "已是最新版本",
        detail: status.checkedAt ? `上次检查于 ${relativeTime(status.checkedAt)}` : undefined,
        note,
        why,
        action: check,
      };
  }
}

// StatusCard is the head of the page: where the update stands and its one
// next step.
function StatusCard({
  status,
  restarting,
  sending,
  onAct,
}: {
  status: SystemUpdateStatus;
  restarting: boolean;
  sending?: Action;
  onAct: (action: Action) => void;
}) {
  const shown = present(status, restarting);
  const { action } = shown;
  // A check runs without changing the phase; its button says so.
  const checking = (status.running && action?.action === "check") || sending === "check";
  const blocked = action?.action === "install" && status.waiting.length > 0;
  return (
    <section
      className={`${styles.updHero} ${styles[shown.tone] ?? ""}`}
      aria-label="更新状态"
      aria-live="polite"
      data-setting="update-status"
    >
      <span className={styles.updMark} aria-hidden="true">
        <shown.icon className={shown.spin ? "spinning" : ""} />
      </span>
      <div className={styles.updText}>
        <h3>{shown.title}</h3>
        {shown.detail && <p>{shown.detail}</p>}
        {shown.note && (
          <p className={styles.updNote}>
            <TriangleAlert aria-hidden="true" />
            {shown.note}
          </p>
        )}
        {shown.progress !== undefined && <Progress value={shown.progress} />}
        {shown.why && (
          <details className={styles.updWhy}>
            <summary>详细信息</summary>
            <pre>{shown.why}</pre>
          </details>
        )}
      </div>
      {action && (
        <button
          className={`${styles.setBtn} primary ${styles.updAction}`}
          disabled={status.running || sending !== undefined || blocked}
          onClick={() => onAct(action.action)}
        >
          {checking && <LoaderCircle className="spinning" aria-hidden="true" />}
          {checking ? "正在检查" : action.label}
        </button>
      )}
    </section>
  );
}

function Progress({ value }: { value: number | null }) {
  return (
    <div
      className={`${styles.updBar} ${value === null ? styles.indeterminate : ""}`}
      role="progressbar"
      aria-label="进度"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value === null ? undefined : Math.round(value * 100)}
    >
      <i style={value === null ? undefined : { width: `${value * 100}%` }} />
    </div>
  );
}

// VersionValue shows a release as it is and a development build with its commit.
function VersionValue({ version }: { version?: string }) {
  if (!version) return <>—</>;
  const parts = versionParts(version);
  return (
    <span className={styles.updVersion} title={version}>
      {parts.release}
      {parts.dev && <Pill outline>开发版</Pill>}
      {parts.commit && <span className={styles.setMono}>{parts.commit}</span>}
    </span>
  );
}

const windowPattern = /^([01]\d|2[0-3]):[0-5]\d$/;

// SettingsGroup holds the automatic download and install. The install window
// is saved when a time field is left with both times valid.
function SettingsGroup({ settings, onError }: { settings: SystemUpdateSettings; onError: (text: string) => void }) {
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState({ start: settings.windowStart, end: settings.windowEnd });
  const [seen, setSeen] = useState(settings);
  if (seen.windowStart !== settings.windowStart || seen.windowEnd !== settings.windowEnd) {
    setSeen(settings);
    setDraft({ start: settings.windowStart, end: settings.windowEnd });
  }
  const valid = windowPattern.test(draft.start) && windowPattern.test(draft.end) && draft.start !== draft.end;
  const save = async (next: SystemUpdateSettings) => {
    setSaving(true);
    onError("");
    try {
      publishSystemUpdate(await updateSystemUpdateSettings(next));
    } catch (caught) {
      onError(errorText(caught, "systemUpdate"));
    } finally {
      setSaving(false);
    }
  };
  const saveWindow = () => {
    if (!valid || (draft.start === settings.windowStart && draft.end === settings.windowEnd)) return;
    void save({ ...settings, windowStart: draft.start, windowEnd: draft.end });
  };
  return (
    <Group title="自动更新" anchor="update-settings">
      <div className={styles.setRow} data-setting="auto-download">
        <div className={styles.setRowText}>
          <span>自动下载新版本</span>
        </div>
        <Switch.Root
          className={styles.setSwitch}
          aria-label="自动下载新版本"
          checked={settings.autoDownload}
          disabled={saving}
          onCheckedChange={(autoDownload) => void save({ ...settings, autoDownload })}
        >
          <Switch.Thumb />
        </Switch.Root>
      </div>
      <div className={styles.setRow} data-setting="auto-install">
        <div className={styles.setRowText}>
          <span>自动安装</span>
          <small>只在设定的时段内安装，安装时服务会重启</small>
        </div>
        <Switch.Root
          className={styles.setSwitch}
          aria-label="自动安装"
          checked={settings.autoInstall}
          disabled={saving}
          onCheckedChange={(autoInstall) => void save({ ...settings, autoInstall })}
        >
          <Switch.Thumb />
        </Switch.Root>
      </div>
      {settings.autoInstall && (
        <div className={styles.setRow} data-setting="install-window">
          <div className={styles.setRowText}>
            <span>安装时段</span>
            {!valid && <small className={styles.updInvalid}>开始与结束要是两个不同的时间</small>}
          </div>
          <span className={styles.updWindow}>
            <input
              type="time"
              className={`${styles.setInput} ${styles.updTime}`}
              aria-label="安装时段开始"
              value={draft.start}
              disabled={saving}
              onChange={(event) => setDraft({ ...draft, start: event.target.value })}
              onBlur={saveWindow}
            />
            <span aria-hidden="true">–</span>
            <input
              type="time"
              className={`${styles.setInput} ${styles.updTime}`}
              aria-label="安装时段结束"
              value={draft.end}
              disabled={saving}
              onChange={(event) => setDraft({ ...draft, end: event.target.value })}
              onBlur={saveWindow}
            />
          </span>
        </div>
      )}
    </Group>
  );
}

const recordLooks: Record<SystemUpdateRecord["kind"], { icon: LucideIcon; tone: string }> = {
  updated: { icon: CircleCheck, tone: "success" },
  "rolled-back": { icon: TriangleAlert, tone: "warning" },
  reverted: { icon: RotateCcw, tone: "neutral" },
  "check-failed": { icon: CircleAlert, tone: "warning" },
  downloaded: { icon: ArrowDownToLine, tone: "neutral" },
  failed: { icon: CircleX, tone: "danger" },
};

function recordText(record: SystemUpdateRecord): string {
  const version = versionLabel(record.version ?? "");
  const from = versionLabel(record.from ?? "");
  switch (record.kind) {
    case "updated":
      return from ? `从 ${from} 更新到 ${version}` : `更新到 ${version}`;
    case "rolled-back":
      return `${version} 安装后检查没有通过，已回到 ${from}`;
    case "reverted":
      return `从 ${from} 回到 ${version}`;
    case "check-failed":
      return "检查更新失败";
    case "downloaded":
      return `下载了 ${version}`;
    case "failed":
      return `安装 ${version} 没有完成`;
  }
}

function RecordRow({ record }: { record: SystemUpdateRecord }) {
  const look = recordLooks[record.kind];
  const meta = [calendarMoment(record.at), byText(record.by), record.problem ? problemText(record.problem) : ""];
  return (
    <div className={`${styles.setRow} ${styles.updRecord}`}>
      <look.icon className={`${styles.updRecordIcon} ${styles[look.tone] ?? ""}`} aria-hidden="true" />
      <div className={styles.setRowText}>
        <span>{recordText(record)}</span>
        <small>{meta.filter(Boolean).join(" · ")}</small>
      </div>
    </div>
  );
}

function ConfirmDialog({
  kind,
  status,
  onClose,
  onConfirm,
}: {
  kind: "install" | "revert";
  status: SystemUpdateStatus;
  onClose: () => void;
  onConfirm: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const current = versionLabel(status.current ?? "");
  const install = kind === "install";
  const version = versionLabel((install ? status.candidate : status.previous) ?? "");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    await onConfirm();
    setBusy(false);
  };
  return (
    <Modal
      title={install ? `安装 A-NAS ${version}？` : `回到 A-NAS ${version}？`}
      description={
        install
          ? `安装期间 A-NAS 的服务会重启，正在进行的上传、终端与相册整理会中断后重连。安装后的检查没有通过时，会自动回到 ${current}。`
          : `A-NAS 的服务会重启，正在进行的上传、终端与相册整理会中断后重连。之后不会再自动安装 ${current}，直到出现更新的版本。`
      }
      busy={busy}
      onClose={onClose}
    >
      <form className={styles.setDialogForm} onSubmit={(event) => void submit(event)}>
        <footer>
          <button type="button" className={styles.setBtn} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className={`${styles.setBtn} primary`} disabled={busy} autoFocus>
            {busy ? "正在开始…" : install ? "安装" : "回到这个版本"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}
