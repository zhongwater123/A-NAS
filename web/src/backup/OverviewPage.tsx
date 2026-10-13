import { ArrowRight, CalendarClock, Camera, FileSearch, HardDrive, History, ShieldAlert } from "lucide-react";

import type { Space } from "../api";
import { BackupCenterGlyph } from "../BackupCenterGlyph";

import { useBackup, type SpaceData } from "./BackupCenter";
import type { Snapshot } from "./backupApi";
import { describeSchedule, formatCount, formatDateTime, formatDay, formatRelative, formatUntil, runErrorText, spaceAudience, spaceLabel } from "./format";
import { Notice, OriginBadge, SpaceIcon } from "./parts";

type Tone = "ok" | "attention" | "danger" | "idle";

interface SpaceState { space: Space; data: SpaceData; tone: Tone }

function stateOf(space: Space, data: SpaceData = {}): SpaceState {
  const schedule = data.schedule;
  let tone: Tone = "idle";
  if (data.offline || (schedule?.enabled && schedule.lastRun?.status === "failed")) tone = "danger";
  else if (schedule?.enabled) tone = "ok";
  else if (data.snapshots?.length) tone = "attention";
  return { space, data, tone };
}

export function OverviewPage() {
  const { spaces, data, navigate } = useBackup();
  const states = spaces.map((space) => stateOf(space, data[space.id]));
  const scheduled = states.filter((state) => state.data.schedule?.enabled);
  const troubled = states.filter((state) => state.tone === "danger");
  const all = states.flatMap((state) => state.data.snapshots ?? []).sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const latest = all[0];
  const upcoming = scheduled
    .map((state) => ({ state, at: state.data.schedule?.nextRunAt }))
    .filter((item): item is { state: SpaceState; at: string } => Boolean(item.at))
    .sort((a, b) => a.at.localeCompare(b.at))[0];
  const loading = states.some((state) => !state.data.schedule && !state.data.error);

  let tone: Tone;
  let headline: string;
  let detail: string;
  if (troubled.some((state) => state.data.offline)) {
    tone = "danger"; headline = "数据卷当前不可用";
    detail = "快照无法读取，定时快照会在数据卷恢复后自动补上。";
  } else if (troubled.length) {
    tone = "danger"; headline = "最近一次定时快照没有成功";
    detail = `${troubled.map((state) => spaceLabel(state.space)).join("、")}的定时快照失败：${runErrorText(troubled[0].data.schedule!)}。15 分钟后会自动重试。`;
  } else if (scheduled.length && scheduled.length === states.length) {
    tone = "ok"; headline = states.length > 1 ? "所有空间都在定时保护中" : "空间正在定时保护中";
    detail = "误删或改坏文件时，可以从任意一份快照里找回。";
  } else if (scheduled.length) {
    tone = "attention"; headline = `${states.length - scheduled.length} 个空间尚未开启定时快照`;
    detail = "开启后，备份中心会按计划自动拍摄快照并只保留最近几份。";
  } else {
    tone = "idle"; headline = "还没有开启定时快照";
    detail = all.length ? "目前只有手动快照。开启定时快照后，误删文件也能找回最近的版本。" : "快照会冻结空间此刻的全部文件，之后可以随时浏览、找回。";
  }

  return (
    <div className="bk-page">
      <header className="bk-page-head">
        <div><h2>概览</h2><p>快照让你在误删、误改之后找回文件。</p></div>
      </header>

      <section className={`bk-hero ${tone}`} aria-label="保护状态">
        <ProtectionRing states={states} />
        <div className="bk-hero-copy">
          <span className={`bk-hero-kicker ${tone}`}>{loading ? "正在检查…" : tone === "ok" ? "已保护" : tone === "danger" ? "需要处理" : tone === "attention" ? "部分保护" : "未开启"}</span>
          <h3>{headline}</h3>
          <p>{detail}</p>
          <dl className="bk-hero-facts">
            <div><dt>最近一次快照</dt><dd>{latest ? <>{formatDateTime(latest.createdAt)}<small>{spaceLabel(spaces.find((space) => space.id === latest.spaceId) ?? { kind: "private" })}</small></> : "暂无"}</dd></div>
            <div><dt>下次定时快照</dt><dd>{upcoming ? <>{formatUntil(upcoming.at)}<small>{spaceLabel(upcoming.state.space)}</small></> : "未安排"}</dd></div>
            <div><dt>快照总数</dt><dd>{formatCount(all.length)}<small>份</small></dd></div>
          </dl>
          <div className="bk-hero-actions">
            <button type="button" className="bk-primary" onClick={() => navigate("find")}><FileSearch />找回文件</button>
            <button type="button" className="bk-secondary" onClick={() => navigate("schedules")}><CalendarClock />{scheduled.length === states.length && states.length ? "定时快照设置" : "开启定时快照"}</button>
          </div>
        </div>
      </section>

      <div className="bk-space-grid">
        {states.map((state) => <SpaceCard key={state.space.id} state={state} />)}
      </div>

      <div className="bk-overview-foot">
        <section className="bk-card bk-recent" aria-label="最近的快照">
          <header className="bk-card-head"><h4><History />最近的快照</h4>{all.length > 0 && <button type="button" className="bk-link" onClick={() => navigate("snapshots")}>全部<ArrowRight /></button>}</header>
          {all.length === 0 ? <p className="bk-muted-line">还没有快照</p> : (
            <ul>
              {all.slice(0, 5).map((snapshot) => {
                const space = spaces.find((item) => item.id === snapshot.spaceId);
                return (
                  <li key={snapshot.id}>
                    <button type="button" onClick={() => navigate("snapshots", { spaceId: snapshot.spaceId, focus: snapshot.id })}>
                      <span className="bk-recent-time">{formatRelative(snapshot.createdAt)}</span>
                      <span className="bk-recent-name">{snapshot.name}</span>
                      <OriginBadge origin={snapshot.origin} />
                      <small>{space ? spaceLabel(space) : ""}</small>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </section>
        <section className="bk-card bk-caveat" aria-label="快照不是备份">
          <span className="bk-caveat-icon"><HardDrive /><ShieldAlert /></span>
          <div>
            <h4>快照和文件在同一块硬盘上</h4>
            <p>快照能找回误删、误改的文件，但硬盘损坏时会和文件一起丢失。备份到外接硬盘或第二块硬盘正在规划中。</p>
            <button type="button" className="bk-link" onClick={() => navigate("external")}>了解外部备份<ArrowRight /></button>
          </div>
        </section>
      </div>
    </div>
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
            cx="60" cy="60" r={radius}
            className={`bk-ring-arc ${state.tone}`}
            strokeDasharray={`${Math.max(segment - gap, 1)} ${circumference}`}
            strokeDashoffset={-(index * segment) - gap / 2}
            transform="rotate(-90 60 60)"
          />
        ))}
      </svg>
      <span className="bk-ring-mark"><BackupCenterGlyph /></span>
    </div>
  );
}

function SpaceCard({ state }: { state: SpaceState }) {
  const { navigate, canManage, createSnapshot } = useBackup();
  const { space, data, tone } = state;
  const schedule = data.schedule;
  const snapshots = data.snapshots;
  const latest = snapshots?.[0];
  const manage = canManage(space);
  return (
    <section className={`bk-card bk-space-card ${tone}`} aria-label={spaceLabel(space)}>
      <header>
        <SpaceIcon space={space} />
        <div className="bk-space-title"><strong>{spaceLabel(space)}</strong><small>{spaceAudience(space)}</small></div>
        <span className={`bk-pill ${tone}`}>
          {data.offline ? "数据卷离线" : !schedule ? "…" : !schedule.enabled ? "未开启定时快照" : schedule.lastRun?.status === "failed" ? `定时失败 · ${runErrorText(schedule)}` : `定时 · ${describeSchedule(schedule)}`}
        </span>
      </header>
      {data.error ? <Notice tone={data.offline ? "warning" : "danger"}>{data.offline ? "数据卷不可用，暂时无法读取快照。" : data.error}</Notice> : (
        <>
          <dl className="bk-space-stats">
            <div><dt>快照</dt><dd>{snapshots ? formatCount(snapshots.length) : "—"}</dd></div>
            <div><dt>最近</dt><dd>{latest ? formatRelative(latest.createdAt) : "—"}</dd></div>
            <div><dt>下次定时</dt><dd>{schedule?.nextRunAt ? formatUntil(schedule.nextRunAt) : "—"}</dd></div>
          </dl>
          <ActivityStrip snapshots={snapshots ?? []} />
        </>
      )}
      <footer>
        <button type="button" className="bk-secondary small" onClick={() => navigate("snapshots", { spaceId: space.id })}><Camera />浏览快照</button>
        {manage
          ? <button type="button" className="bk-secondary small" disabled={data.offline} onClick={() => createSnapshot(space)}>立即创建快照</button>
          : <span className="bk-muted-line">共享空间的快照由管理员创建</span>}
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
    const count = snapshots.filter((snapshot) => { const at = new Date(snapshot.createdAt); return at >= day && at < next; }).length;
    return { day, count };
  });
  const peak = Math.max(1, ...days.map((item) => item.count));
  return (
    <div className="bk-strip">
      <div className="bk-strip-bars" role="img" aria-label={`最近 ${stripDays} 天有 ${days.filter((item) => item.count).length} 天拍了快照`}>
        {days.map((item) => (
          <span key={item.day.toISOString()} title={`${formatDay(item.day)}：${item.count ? `${item.count} 份快照` : "没有快照"}`} className={item.count ? "filled" : ""} style={{ height: `${item.count ? 30 + (70 * item.count) / peak : 18}%` }} />
        ))}
      </div>
      <div className="bk-strip-axis"><span>{stripDays} 天前</span><span>今天</span></div>
    </div>
  );
}
