import { CalendarClock, CircleAlert, CircleCheck, Clock3, Info, Minus, Plus } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import type { Space } from "../api";

import { useBackup } from "./BackupCenter";
import { maxKeep, messageOf, saveSchedule, type Frequency, type ScheduleSettings, type SnapshotSchedule } from "./backupApi";
import { describeRetention, describeSchedule, formatDateTime, formatUntil, runErrorText, spaceAudience, spaceLabel, weekdayShort } from "./format";
import { SpaceIcon, Spinner, Switch } from "./parts";

const frequencies: { value: Frequency; label: string }[] = [
  { value: "hourly", label: "每小时" },
  { value: "daily", label: "每天" },
  { value: "weekly", label: "每周" },
];
// What a schedule keeps by default: a day of hours, two weeks of days, two months of weeks.
const suggestedKeep: Record<Frequency, number> = { hourly: 24, daily: 14, weekly: 8 };
// Monday first, as calendars in China are.
const weekOrder = [1, 2, 3, 4, 5, 6, 0];
const hours = Array.from({ length: 24 }, (_, hour) => String(hour).padStart(2, "0"));
const fiveMinutes = Array.from({ length: 12 }, (_, step) => String(step * 5).padStart(2, "0"));

const settingsOf = (schedule: SnapshotSchedule): ScheduleSettings => ({
  enabled: schedule.enabled, frequency: schedule.frequency, time: schedule.time, weekday: schedule.weekday, keep: schedule.keep,
});
const sameSettings = (a: ScheduleSettings, b: ScheduleSettings) =>
  a.enabled === b.enabled && a.frequency === b.frequency && a.time === b.time && a.weekday === b.weekday && a.keep === b.keep;

export function SchedulesPage() {
  const { spaces, data } = useBackup();
  return (
    <div className="bk-page bk-schedules-page">
      <header className="bk-page-head">
        <div><h2>定时快照</h2><p>按计划自动拍摄快照，只保留最近几份。</p></div>
      </header>
      <div className="bk-schedule-list">
        {spaces.map((space) => {
          const schedule = data[space.id]?.schedule;
          return schedule
            ? <ScheduleCard key={space.id} space={space} schedule={schedule} />
            : <section key={space.id} className="bk-card bk-schedule-card loading"><Spinner small />正在读取{spaceLabel(space)}的设置…</section>;
        })}
      </div>
      <section className="bk-card bk-about" aria-label="关于定时快照">
        <h4><Info />定时快照如何工作</h4>
        <ul>
          <li>时间按这台 A-NAS 的时区计算。设备关机时错过的时间点，开机后会补拍一次，而不是补拍每一次。</li>
          <li>每次成功拍摄后，只保留最近的若干份定时快照，更早的会被自动删除。</li>
          <li>手动快照不会被自动删除；想长期保留某份定时快照，可以在快照列表中点“保留”。</li>
          <li>如果拍摄失败（例如数据卷离线），会在 15 分钟后重试，直到下一个时间点。</li>
        </ul>
      </section>
    </div>
  );
}

function ScheduleCard({ space, schedule }: { space: Space; schedule: SnapshotSchedule }) {
  const { setSchedule, toast, data } = useBackup();
  const [draft, setDraft] = useState<ScheduleSettings>(() => settingsOf(schedule));
  const [saving, setSaving] = useState(false);
  const saved = settingsOf(schedule);
  const dirty = !sameSettings(draft, saved);
  const readOnly = !schedule.canManage;
  const lastSaved = useRef(saved);

  // Follow what the server reports unless the person is editing.
  useEffect(() => {
    const next = settingsOf(schedule);
    setDraft((current) => (sameSettings(current, lastSaved.current) ? next : current));
    lastSaved.current = next;
  }, [schedule]);

  const update = (change: Partial<ScheduleSettings>) => setDraft((current) => {
    const next = { ...current, ...change };
    // A new frequency brings its usual retention unless the person chose one.
    if (change.frequency && change.frequency !== current.frequency && current.keep === suggestedKeep[current.frequency]) next.keep = suggestedKeep[change.frequency];
    return next;
  });
  const [hour, minute] = draft.time.split(":");
  const minutes = fiveMinutes.includes(minute) ? fiveMinutes : [...fiveMinutes, minute].sort();

  const save = async () => {
    setSaving(true);
    try {
      const next = await saveSchedule(space.id, draft);
      setSchedule(next);
      setDraft(settingsOf(next));
      toast(next.enabled ? `${spaceLabel(space)}将${describeSchedule(next)}拍摄快照` : `已关闭${spaceLabel(space)}的定时快照`);
    } catch (caught) {
      toast(messageOf(caught), "error");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className={`bk-card bk-schedule-card${draft.enabled ? " on" : ""}`} aria-label={`${spaceLabel(space)}的定时快照`}>
      <header>
        <SpaceIcon space={space} />
        <div className="bk-space-title"><strong>{spaceLabel(space)}</strong><small>{spaceAudience(space)}</small></div>
        <Switch checked={draft.enabled} disabled={readOnly || saving} label={`开启${spaceLabel(space)}的定时快照`} onChange={(enabled) => update({ enabled })} />
      </header>

      {draft.enabled ? (
        <div className="bk-schedule-form">
          <div className="bk-row">
            <span className="bk-row-label">频率</span>
            <div className="bk-segmented small" role="radiogroup" aria-label="频率">
              {frequencies.map((item) => (
                <button key={item.value} type="button" role="radio" aria-checked={draft.frequency === item.value} className={draft.frequency === item.value ? "active" : ""} disabled={readOnly || saving} onClick={() => update({ frequency: item.value })}>{item.label}</button>
              ))}
            </div>
          </div>
          {draft.frequency === "weekly" && (
            <div className="bk-row">
              <span className="bk-row-label">星期</span>
              <div className="bk-weekdays" role="radiogroup" aria-label="星期">
                {weekOrder.map((day) => (
                  <button key={day} type="button" role="radio" aria-checked={draft.weekday === day} aria-label={`周${weekdayShort[day]}`} className={draft.weekday === day ? "active" : ""} disabled={readOnly || saving} onClick={() => update({ weekday: day })}>{weekdayShort[day]}</button>
                ))}
              </div>
            </div>
          )}
          <div className="bk-row">
            <span className="bk-row-label">时间</span>
            <div className="bk-time">
              {draft.frequency === "hourly" ? (
                <>每小时的第<select aria-label="分钟" value={minute} disabled={readOnly || saving} onChange={(event) => update({ time: `${hour}:${event.target.value}` })}>{minutes.map((value) => <option key={value} value={value}>{value}</option>)}</select>分</>
              ) : (
                <>
                  <select aria-label="小时" value={hour} disabled={readOnly || saving} onChange={(event) => update({ time: `${event.target.value}:${minute}` })}>{hours.map((value) => <option key={value} value={value}>{value}</option>)}</select>
                  <span className="bk-time-colon">:</span>
                  <select aria-label="分钟" value={minute} disabled={readOnly || saving} onChange={(event) => update({ time: `${hour}:${event.target.value}` })}>{minutes.map((value) => <option key={value} value={value}>{value}</option>)}</select>
                </>
              )}
            </div>
          </div>
          <div className="bk-row">
            <span className="bk-row-label">保留</span>
            <div className="bk-keep">
              <div className="bk-stepper">
                <button type="button" aria-label="少保留一份" disabled={readOnly || saving || draft.keep <= 1} onClick={() => update({ keep: draft.keep - 1 })}><Minus /></button>
                <input
                  type="number" min={1} max={maxKeep} value={draft.keep} disabled={readOnly || saving} aria-label="保留份数"
                  onChange={(event) => { const value = Math.round(Number(event.target.value)); if (Number.isFinite(value)) update({ keep: Math.min(maxKeep, Math.max(1, value)) }); }}
                />
                <button type="button" aria-label="多保留一份" disabled={readOnly || saving || draft.keep >= maxKeep} onClick={() => update({ keep: draft.keep + 1 })}><Plus /></button>
              </div>
              <span>份定时快照<small>{describeRetention(draft.frequency, draft.keep)}</small></span>
            </div>
          </div>
        </div>
      ) : (
        <p className="bk-schedule-off">{readOnly ? "管理员尚未为共享空间开启定时快照；开启后你可以浏览和恢复其中的文件。" : "开启后按计划自动拍摄快照，并只保留最近几份。"}</p>
      )}

      <ScheduleStatus schedule={schedule} offline={Boolean(data[space.id]?.offline)} />

      {readOnly && draft.enabled && <p className="bk-readonly-note">共享空间的定时快照由管理员设置，你可以浏览和恢复其中的文件。</p>}
      {dirty && !readOnly && (
        <footer className="bk-save-bar">
          <span>{draft.enabled ? `将${describeSchedule(draft)}拍摄，保留 ${draft.keep} 份` : "将关闭定时快照，已有快照会保留"}</span>
          <button type="button" className="bk-secondary small" disabled={saving} onClick={() => setDraft(saved)}>还原</button>
          <button type="button" className="bk-primary small" disabled={saving} onClick={() => void save()}>{saving ? <><Spinner small />保存中…</> : "保存"}</button>
        </footer>
      )}
    </section>
  );
}

function ScheduleStatus({ schedule, offline }: { schedule: SnapshotSchedule; offline: boolean }) {
  const run = schedule.lastRun;
  if (!schedule.enabled && !run) return null;
  return (
    <div className="bk-schedule-status">
      {schedule.enabled && schedule.nextRunAt && <span><CalendarClock />下次：{formatUntil(schedule.nextRunAt)}{offline ? "（数据卷离线）" : ""}</span>}
      {run && (run.status === "succeeded"
        ? <span className="ok"><CircleCheck />上次：{formatDateTime(run.at)} 成功</span>
        : <span className="failed"><CircleAlert />上次：{formatDateTime(run.at)} 失败，{runErrorText(schedule)}</span>)}
      {!schedule.enabled && <span><Clock3 />已关闭</span>}
    </div>
  );
}
