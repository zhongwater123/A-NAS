import {
  ArrowRight,
  CalendarClock,
  Camera,
  CircleCheck,
  FileSearch,
  HardDrive,
  History,
  ShieldAlert,
} from "lucide-react";

import type { Space } from "../api";
import { BackupCenterGlyph } from "../BackupCenterGlyph";

import { useBackup, type SpaceData } from "./BackupCenter";
import type { Snapshot } from "./backupApi";
import {
  describeSchedule,
  formatCount,
  formatDateTime,
  formatDay,
  formatRelative,
  formatShortDay,
  formatUntil,
  runErrorText,
  spaceAudience,
  spaceLabel,
} from "./format";
import { Notice, SpaceIcon } from "./parts";

type Tone = "ok" | "attention" | "danger" | "idle";

interface SpaceState {
  space: Space;
  data: SpaceData;
  tone: Tone;
}

function stateOf(space: Space, data: SpaceData = {}): SpaceState {
  const schedule = data.schedule;
  let tone: Tone = "idle";
  if (data.offline || (schedule?.enabled && schedule.lastRun?.status === "failed")) tone = "danger";
  else if (schedule?.enabled) tone = "ok";
  else if (data.snapshots?.length) tone = "attention";
  return { space, data, tone };
}

const kicker: Record<Tone, string> = { ok: "防误删已开启", attention: "部分开启", danger: "需要处理", idle: "未开启" };

export function OverviewPage() {
  const { spaces, data, navigate } = useBackup();
  const states = spaces.map((space) => stateOf(space, data[space.id]));
  const scheduled = states.filter((state) => state.data.schedule?.enabled);
  const unscheduled = states.filter((state) => state.data.schedule && !state.data.schedule.enabled);
  // Only the spaces this person can set up are worth a button.
  const manageable = unscheduled.filter((state) => state.data.schedule?.canManage);
  const troubled = states.filter((state) => state.tone === "danger");
  const all = states
    .flatMap((state) => state.data.snapshots ?? [])
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const latest = all[0];
  const oldest = all.at(-1);
  const upcoming = scheduled
    .map((state) => ({ state, at: state.data.schedule?.nextRunAt }))
    .filter((item): item is { state: SpaceState; at: string } => Boolean(item.at))
    .sort((a, b) => a.at.localeCompare(b.at))[0];
  // Until every space has answered, the page says nothing about protection:
  // a moment of "not protected" would alarm people for no reason.
  const loading = states.some((state) => !state.data.schedule && !state.data.error);
  const plans = new Set(scheduled.map((state) => describeSchedule(state.data.schedule!)));

  let tone: Tone;
  let headline: string;
  let detail: string;
  if (loading) {
    tone = "idle";
    headline = "";
    detail = "";
  } else if (troubled.some((state) => state.data.offline)) {
    tone = "danger";
    headline = "数据卷当前不可用";
    detail = "快照暂时无法读取。数据卷恢复后，错过的定时快照会自动补拍一份。";
  } else if (troubled.length) {
    tone = "danger";
    headline = "最近一次定时快照没有成功";
    detail = `${troubled.map((state) => spaceLabel(state.space)).join("、")}的定时快照失败：${runErrorText(troubled[0].data.schedule!)}。15 分钟后会自动重试。`;
  } else if (scheduled.length && !unscheduled.length) {
    tone = "ok";
    headline = "误删、改坏的文件都能找回";
    const who = states.length > 1 ? "每个空间" : spaceLabel(states[0].space);
    detail = `${who}${plans.size === 1 ? `${[...plans][0]} 自动拍一份快照` : "都会按计划自动拍快照"}，只保留最近的一部分，不用操心清理。`;
  } else if (scheduled.length) {
    tone = "attention";
    headline = `${unscheduled.map((state) => spaceLabel(state.space)).join("、")}还没有开启定时快照`;
    detail = manageable.length
      ? "开启后，那里误删、改坏的文件也能从快照里找回。"
      : "共享空间的定时快照由管理员设置，可以请管理员开启。";
  } else {
    tone = "idle";
    headline = "还没有开启定时快照";
    detail = all.length
      ? "目前只有手动快照。开启定时快照后，每天自动留一份，误删的文件也能找回最近的版本。"
      : "快照会定时冻结空间里的全部文件，之后误删、改坏的文件都能从中找回。";
  }
  const needsSchedule = !loading && manageable.length > 0 && !troubled.length;

  return (
    <div className="bk-page">
      <header className="bk-page-head">
        <div>
          <h2>概览</h2>
          <p>快照让你在误删、误改之后找回文件。</p>
        </div>
      </header>

      <section className={`bk-hero ${tone}`} aria-label="保护状态" aria-busy={loading}>
        <ProtectionRing states={loading ? [] : states} />
        <div className="bk-hero-copy">
          <span className={`bk-hero-kicker ${tone}`}>{loading ? "正在检查…" : kicker[tone]}</span>
          {loading ? (
            <div className="bk-skeleton" aria-hidden="true">
              <i className="wide" />
              <i />
            </div>
          ) : (
            <>
              <h3>{headline}</h3>
              <p>{detail}</p>
            </>
          )}
          <dl className="bk-hero-facts">
            <div>
              <dt>最近一次快照</dt>
              <dd>
                {latest ? (
                  <>
                    {formatDateTime(latest.createdAt)}
                    <small>
                      {spaceLabel(spaces.find((space) => space.id === latest.spaceId) ?? { kind: "private" })}
                    </small>
                  </>
                ) : loading ? (
                  "—"
                ) : (
                  "暂无"
                )}
              </dd>
            </div>
            <div>
              <dt>下次定时快照</dt>
              <dd>
                {upcoming ? (
                  <>
                    {formatUntil(upcoming.at)}
                    <small>{spaceLabel(upcoming.state.space)}</small>
                  </>
                ) : loading ? (
                  "—"
                ) : (
                  "未安排"
                )}
              </dd>
            </div>
            <div>
              <dt>最早可回到</dt>
              <dd>
                {oldest ? (
                  <>
                    {formatDay(oldest.createdAt)}
                    <small>共 {formatCount(all.length)} 份快照</small>
                  </>
                ) : loading ? (
                  "—"
                ) : (
                  "暂无"
                )}
              </dd>
            </div>
          </dl>
          <div className="bk-hero-actions">
            {needsSchedule ? (
              <>
                <button
                  type="button"
                  className="bk-primary"
                  onClick={() => navigate("schedules", { spaceId: manageable[0].space.id })}
                >
                  <CalendarClock />
                  开启定时快照
                </button>
                <button type="button" className="bk-secondary" onClick={() => navigate("find")}>
                  <FileSearch />
                  找回文件
                </button>
              </>
            ) : (
              <>
                <button type="button" className="bk-primary" onClick={() => navigate("find")}>
                  <FileSearch />
                  找回文件
                </button>
                <button type="button" className="bk-secondary" onClick={() => navigate("snapshots")}>
                  <History />
                  按时间浏览
                </button>
              </>
            )}
          </div>
        </div>
      </section>

      <Coverage protectedNow={all.length > 0} />

      <div className="bk-space-grid">
        {states.map((state) => (
          <SpaceCard key={state.space.id} state={state} />
        ))}
      </div>
    </div>
  );
}

// Coverage says plainly what snapshots guard against and what they do not,
// next to the status rather than at the bottom of the page, so "protected"
// is never read as "backed up".
function Coverage({ protectedNow }: { protectedNow: boolean }) {
  const { navigate } = useBackup();
  return (
    <section className="bk-coverage" aria-label="能防范什么">
      <div className={`bk-coverage-item ${protectedNow ? "ok" : "off"}`}>
        <span className="bk-coverage-icon">{protectedNow ? <CircleCheck /> : <Camera />}</span>
        <div>
          <strong>误删、误改{protectedNow && <em className="ok">已防护</em>}</strong>
          <p>
            {protectedNow
              ? "删掉的到“找回文件”恢复；改坏的文档，在文件管理中右键它，选“历史版本”换回。"
              : "有了快照之后，就能找回误删、改坏的文件。"}
          </p>
        </div>
      </div>
      <div className="bk-coverage-item risk" role="note" aria-label="快照不是备份">
        <span className="bk-coverage-icon">
          <HardDrive />
          <ShieldAlert />
        </span>
        <div>
          <strong>
            硬盘损坏<em>尚未防护</em>
          </strong>
          <p>快照和文件在同一块硬盘上，硬盘坏了会一起丢失。备份到另一块硬盘还在规划中。</p>
        </div>
        <button type="button" className="bk-link" onClick={() => navigate("external")}>
          了解
          <ArrowRight />
        </button>
      </div>
    </section>
  );
}

// ProtectionRing draws one arc per space around the backup center's mark,
// coloured by how that space is protected.
function ProtectionRing({ states }: { states: SpaceState[] }) {
  const radius = 46;
  const circumference = 2 * Math.PI * radius;
  const gap = states.length > 1 ? 10 : 0;
  const segment = states.length ? circumference / states.length : circumference;
  return (
    <div className="bk-ring" aria-hidden="true">
      <svg viewBox="0 0 120 120">
        <circle cx="60" cy="60" r={radius} className="bk-ring-track" />
        {states.map((state, index) => (
          <circle
            key={state.space.id}
            cx="60"
            cy="60"
            r={radius}
            className={`bk-ring-arc ${state.tone}`}
            strokeDasharray={`${Math.max(segment - gap, 1)} ${circumference}`}
            strokeDashoffset={-(index * segment) - gap / 2}
            transform="rotate(-90 60 60)"
          />
        ))}
      </svg>
      <span className="bk-ring-mark">
        <BackupCenterGlyph />
      </span>
    </div>
  );
}

function SpaceCard({ state }: { state: SpaceState }) {
  const { navigate, canManage, createSnapshot } = useBackup();
  const { space, data, tone } = state;
  const schedule = data.schedule;
  const snapshots = data.snapshots;
  const latest = snapshots?.[0];
  const oldest = snapshots?.at(-1);
  const manage = canManage(space);
  return (
    <section className={`bk-card bk-space-card ${tone}`} aria-label={spaceLabel(space)}>
      <header>
        <SpaceIcon space={space} />
        <div className="bk-space-title">
          <strong>{spaceLabel(space)}</strong>
          <small>{spaceAudience(space)}</small>
        </div>
        <span className={`bk-pill ${tone}`}>
          {data.offline
            ? "数据卷离线"
            : !schedule
              ? "…"
              : !schedule.enabled
                ? "未开启定时快照"
                : schedule.lastRun?.status === "failed"
                  ? `定时失败 · ${runErrorText(schedule)}`
                  : `定时 · ${describeSchedule(schedule)}`}
        </span>
      </header>
      {data.error ? (
        <Notice tone={data.offline ? "warning" : "danger"}>
          {data.offline ? "数据卷不可用，暂时无法读取快照。" : data.error}
        </Notice>
      ) : (
        <>
          <dl className="bk-space-stats">
            <div>
              <dt>最近一次</dt>
              <dd>{latest ? formatRelative(latest.createdAt) : "—"}</dd>
            </div>
            <div>
              <dt>最早可回到</dt>
              <dd>{oldest ? formatShortDay(oldest.createdAt) : "—"}</dd>
            </div>
            <div>
              <dt>下次定时</dt>
              <dd>{schedule?.nextRunAt ? formatUntil(schedule.nextRunAt) : "—"}</dd>
            </div>
          </dl>
          <ActivityStrip snapshots={snapshots ?? []} />
        </>
      )}
      <footer>
        {manage && schedule && !schedule.enabled && !data.offline ? (
          <button
            type="button"
            className="bk-primary small"
            onClick={() => navigate("schedules", { spaceId: space.id })}
          >
            <CalendarClock />
            开启定时快照
          </button>
        ) : null}
        <button
          type="button"
          className="bk-secondary small"
          disabled={!snapshots?.length}
          onClick={() => navigate("snapshots", { spaceId: space.id })}
        >
          <History />
          浏览快照
        </button>
        {manage ? (
          <button
            type="button"
            className="bk-secondary small"
            disabled={data.offline}
            onClick={() => createSnapshot(space)}
          >
            <Camera />
            立即创建快照
          </button>
        ) : (
          <span className="bk-muted-line">共享空间的快照由管理员创建</span>
        )}
      </footer>
    </section>
  );
}

const stripDays = 14;

// ActivityStrip shows how many snapshots each of the last two weeks holds.
function ActivityStrip({ snapshots }: { snapshots: Snapshot[] }) {
  const today = new Date();
  const days = Array.from({ length: stripDays }, (_, index) => {
    const day = new Date(today.getFullYear(), today.getMonth(), today.getDate() - (stripDays - 1 - index));
    const next = new Date(day.getFullYear(), day.getMonth(), day.getDate() + 1);
    const count = snapshots.filter((snapshot) => {
      const at = new Date(snapshot.createdAt);
      return at >= day && at < next;
    }).length;
    return { day, count };
  });
  const peak = Math.max(1, ...days.map((item) => item.count));
  return (
    <div className="bk-strip">
      <div
        className="bk-strip-bars"
        role="img"
        aria-label={`最近 ${stripDays} 天有 ${days.filter((item) => item.count).length} 天拍了快照`}
      >
        {days.map((item) => (
          <span
            key={item.day.toISOString()}
            title={`${formatDay(item.day)}：${item.count ? `${item.count} 份快照` : "没有快照"}`}
            className={item.count ? "filled" : ""}
            style={{ height: `${item.count ? 30 + (70 * item.count) / peak : 18}%` }}
          />
        ))}
      </div>
      <div className="bk-strip-axis">
        <span>{stripDays} 天前</span>
        <span>共 {formatCount(snapshots.length)} 份快照</span>
        <span>今天</span>
      </div>
    </div>
  );
}
