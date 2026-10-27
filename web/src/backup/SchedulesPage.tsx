import { CalendarClock, CircleAlert, CircleCheck, Clock3, Info, SlidersHorizontal } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import type { Space } from "../api.generated";
import {
  type Frequency,
  maxKeep,
  messageOf,
  type Retention,
  type RetentionTiers,
  type ScheduleSettings,
  type SnapshotSchedule,
  saveSchedule,
} from "./api";
import { useBackup } from "./BackupCenter";
import {
  describeRetention,
  describeSchedule,
  formatDateTime,
  formatUntil,
  noTiers,
  runErrorText,
  spaceAudience,
  spaceLabel,
  suggestedTiers,
  type Tier,
  tierLabel,
  tiersFor,
  tierTotal,
  weekdayShort,
} from "./format";
import { SpaceIcon, Spinner, Stepper, Switch } from "./parts";

const frequencies: { value: Frequency; label: string }[] = [
  { value: "hourly", label: "每小时" },
  { value: "daily", label: "每天" },
  { value: "weekly", label: "每周" },
];
// What a schedule keeps by default: a day of hours, two weeks of days, two months of weeks.
const suggestedKeep: Record<Frequency, number> = { hourly: 24, daily: 14, weekly: 8 };
const retentions: { value: Retention; label: string }[] = [
  { value: "count", label: "固定份数" },
  { value: "tiered", label: "智能保留" },
];
// Monday first, as calendars in China are.
const weekOrder = [1, 2, 3, 4, 5, 6, 0];
const hours = Array.from({ length: 24 }, (_, hour) => String(hour).padStart(2, "0"));
const fiveMinutes = Array.from({ length: 12 }, (_, step) => String(step * 5).padStart(2, "0"));

const settingsOf = (schedule: SnapshotSchedule): ScheduleSettings => ({
  enabled: schedule.enabled,
  frequency: schedule.frequency,
  time: schedule.time,
  weekday: schedule.weekday,
  keep: schedule.keep,
  retention: schedule.retention ?? "count",
  tiers: schedule.retention === "tiered" ? schedule.tiers : suggestedTiers(schedule.frequency),
});
// saved is what the server stores: tiers only under tiered retention. The
// draft keeps its tiers while count is chosen, so switching back loses nothing.
const saved = (settings: ScheduleSettings): ScheduleSettings =>
  settings.retention === "tiered" ? settings : { ...settings, tiers: noTiers };
const sameTiers = (a: RetentionTiers, b: RetentionTiers) =>
  a.hourly === b.hourly && a.daily === b.daily && a.weekly === b.weekly && a.monthly === b.monthly;
const sameSettings = (left: ScheduleSettings, right: ScheduleSettings) => {
  const [a, b] = [saved(left), saved(right)];
  return (
    a.enabled === b.enabled &&
    a.frequency === b.frequency &&
    a.time === b.time &&
    a.weekday === b.weekday &&
    a.keep === b.keep &&
    a.retention === b.retention &&
    sameTiers(a.tiers, b.tiers)
  );
};

type Plan = Pick<ScheduleSettings, "frequency" | "retention" | "keep" | "tiers">;

// Plans name the common choices by what they let people do, so nobody has to
// work out frequencies and retention first. Each keeps the time already set.
const plans: { id: string; title: string; every: string; reach: string; tag?: string; plan: Plan }[] = [
  {
    id: "standard",
    title: "标准",
    tag: "默认",
    every: "每天一份",
    reach: "可回到两周内任一天",
    plan: { frequency: "daily", retention: "count", keep: suggestedKeep.daily, tiers: suggestedTiers("daily") },
  },
  {
    id: "long",
    title: "长期",
    every: "每天一份，越久越疏",
    reach: "最远可回到约一年前",
    plan: { frequency: "daily", retention: "tiered", keep: suggestedKeep.daily, tiers: suggestedTiers("daily") },
  },
  {
    id: "frequent",
    title: "频繁",
    every: "每小时一份，越久越疏",
    reach: "最远可回到约一年前",
    plan: { frequency: "hourly", retention: "tiered", keep: suggestedKeep.hourly, tiers: suggestedTiers("hourly") },
  },
];

// planOf names the plan a schedule follows, if any; the time does not matter.
function planOf(settings: ScheduleSettings): string | undefined {
  return plans.find(
    ({ plan }) =>
      plan.frequency === settings.frequency &&
      plan.retention === settings.retention &&
      (plan.retention === "count" ? plan.keep === settings.keep : sameTiers(plan.tiers, settings.tiers)),
  )?.id;
}

// tiersAfter carries tiers over to a new frequency: suggested ones follow the
// frequency; the person's own lose only the tiers finer than it.
function tiersAfter(tiers: RetentionTiers, from: Frequency, to: Frequency): RetentionTiers {
  if (sameTiers(tiers, suggestedTiers(from))) return suggestedTiers(to);
  const usable = tiersFor(to);
  const kept = { ...noTiers, ...Object.fromEntries(usable.map((tier) => [tier, tiers[tier]])) };
  return tierTotal(kept) ? kept : suggestedTiers(to);
}

export function SchedulesPage() {
  const { spaces, data } = useBackup();
  return (
    <div className="bk-page bk-schedules-page">
      <header className="bk-page-head">
        <div>
          <h2>定时快照</h2>
          <p>按计划自动拍摄快照，并自动清理较早的。选一个方案即可，之后随时可以改。</p>
        </div>
      </header>
      <div className="bk-schedule-list">
        {spaces.map((space) => {
          const schedule = data[space.id]?.schedule;
          return schedule ? (
            <ScheduleCard key={space.id} space={space} schedule={schedule} />
          ) : (
            <section key={space.id} className="bk-card bk-schedule-card loading">
              <Spinner small />
              正在读取{spaceLabel(space)}的设置…
            </section>
          );
        })}
      </div>
      <section className="bk-card bk-about" aria-label="关于定时快照">
        <h4>
          <Info />
          定时快照如何工作
        </h4>
        <ul>
          <li>时间按这台 A-NAS 的时区计算。设备关机时错过的时间点，开机后会补拍一次，而不是补拍每一次。</li>
          <li>
            每次成功拍摄后，自动删除不再需要的定时快照。“标准”只留最近 14
            份；“长期”和“频繁”使用智能保留：近期每天或每小时一份，往前每周、每月各留一份，用更少的快照回到更久以前。
          </li>
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
  // custom shows the full editor; a schedule outside the plans starts there.
  const [custom, setCustom] = useState(() => !planOf(settingsOf(schedule)));
  const stored = settingsOf(schedule);
  const dirty = !sameSettings(draft, stored);
  const readOnly = !schedule.canManage;
  const lastSaved = useRef(stored);

  // Follow what the server reports unless the person is editing.
  useEffect(() => {
    const next = settingsOf(schedule);
    setDraft((current) => (sameSettings(current, lastSaved.current) ? next : current));
    lastSaved.current = next;
  }, [schedule]);
  const chosen = custom ? undefined : planOf(draft);

  const update = (change: Partial<ScheduleSettings>) =>
    setDraft((current) => {
      const next = { ...current, ...change };
      // A new frequency brings its usual retention unless the person chose one.
      if (change.frequency && change.frequency !== current.frequency) {
        if (current.keep === suggestedKeep[current.frequency]) next.keep = suggestedKeep[change.frequency];
        next.tiers = tiersAfter(current.tiers, current.frequency, change.frequency);
      }
      return next;
    });
  const busy = readOnly || saving;
  const [hour, minute] = draft.time.split(":");
  const minutes = fiveMinutes.includes(minute) ? fiveMinutes : [...fiveMinutes, minute].sort();

  const save = async () => {
    setSaving(true);
    try {
      const next = await saveSchedule(space.id, saved(draft));
      setSchedule(next);
      setDraft(settingsOf(next));
      toast(
        next.enabled
          ? `${spaceLabel(space)}将${describeSchedule(next)}拍摄快照`
          : `已关闭${spaceLabel(space)}的定时快照`,
      );
    } catch (caught) {
      toast(messageOf(caught), "error");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section
      className={`bk-card bk-schedule-card${draft.enabled ? " on" : ""}`}
      aria-label={`${spaceLabel(space)}的定时快照`}
    >
      <header>
        <SpaceIcon space={space} />
        <div className="bk-space-title">
          <strong>{spaceLabel(space)}</strong>
          <small>{spaceAudience(space)}</small>
        </div>
        <Switch
          checked={draft.enabled}
          disabled={busy}
          label={`开启${spaceLabel(space)}的定时快照`}
          onChange={(enabled) => update({ enabled })}
        />
      </header>

      {draft.enabled && readOnly ? (
        <div className="bk-schedule-summary">
          <p>
            <strong>{describeSchedule(draft)}</strong> 自动拍一份 · {describeRetention(draft)}
          </p>
          <RetentionStrip settings={draft} />
        </div>
      ) : draft.enabled ? (
        <div className="bk-schedule-form">
          <div className="bk-plans" role="radiogroup" aria-label="保护方案">
            {plans.map((item) => (
              <button
                key={item.id}
                type="button"
                role="radio"
                aria-checked={chosen === item.id}
                className={`bk-plan${chosen === item.id ? " active" : ""}`}
                disabled={busy}
                onClick={() => {
                  setCustom(false);
                  setDraft((current) => ({ ...current, ...item.plan }));
                }}
              >
                <span className="bk-plan-radio" aria-hidden="true" />
                <strong>
                  {item.title}
                  {item.tag && <em>{item.tag}</em>}
                </strong>
                <small>{item.every}</small>
                <small className="reach">{item.reach}</small>
              </button>
            ))}
            <button
              type="button"
              role="radio"
              aria-checked={!chosen}
              className={`bk-plan${chosen ? "" : " active"}`}
              disabled={busy}
              onClick={() => setCustom(true)}
            >
              <span className="bk-plan-radio" aria-hidden="true" />
              <strong>
                <SlidersHorizontal />
                自定义
              </strong>
              <small>自己设定拍摄频率</small>
              <small className="reach">和保留方式</small>
            </button>
          </div>
          {!chosen && (
            <div className="bk-row">
              <span className="bk-row-label">频率</span>
              <div className="bk-segmented small" role="radiogroup" aria-label="频率">
                {frequencies.map((item) => (
                  <button
                    key={item.value}
                    type="button"
                    role="radio"
                    aria-checked={draft.frequency === item.value}
                    className={draft.frequency === item.value ? "active" : ""}
                    disabled={busy}
                    onClick={() => update({ frequency: item.value })}
                  >
                    {item.label}
                  </button>
                ))}
              </div>
            </div>
          )}
          {draft.frequency === "weekly" && (
            <div className="bk-row">
              <span className="bk-row-label">星期</span>
              <div className="bk-weekdays" role="radiogroup" aria-label="星期">
                {weekOrder.map((day) => (
                  <button
                    key={day}
                    type="button"
                    role="radio"
                    aria-checked={draft.weekday === day}
                    aria-label={`周${weekdayShort[day]}`}
                    className={draft.weekday === day ? "active" : ""}
                    disabled={busy}
                    onClick={() => update({ weekday: day })}
                  >
                    {weekdayShort[day]}
                  </button>
                ))}
              </div>
            </div>
          )}
          <div className="bk-row">
            <span className="bk-row-label">时间</span>
            <div className="bk-time">
              {draft.frequency === "hourly" ? (
                <>
                  每小时的第
                  <select
                    aria-label="分钟"
                    value={minute}
                    disabled={busy}
                    onChange={(event) => update({ time: `${hour}:${event.target.value}` })}
                  >
                    {minutes.map((value) => (
                      <option key={value} value={value}>
                        {value}
                      </option>
                    ))}
                  </select>
                  分
                </>
              ) : (
                <>
                  <select
                    aria-label="小时"
                    value={hour}
                    disabled={busy}
                    onChange={(event) => update({ time: `${event.target.value}:${minute}` })}
                  >
                    {hours.map((value) => (
                      <option key={value} value={value}>
                        {value}
                      </option>
                    ))}
                  </select>
                  <span className="bk-time-colon">:</span>
                  <select
                    aria-label="分钟"
                    value={minute}
                    disabled={busy}
                    onChange={(event) => update({ time: `${hour}:${event.target.value}` })}
                  >
                    {minutes.map((value) => (
                      <option key={value} value={value}>
                        {value}
                      </option>
                    ))}
                  </select>
                </>
              )}
            </div>
          </div>
          <div className="bk-row top">
            <span className="bk-row-label">{chosen ? "可回到" : "保留"}</span>
            <div className="bk-retention">
              {!chosen && (
                <>
                  <div className="bk-segmented small" role="radiogroup" aria-label="保留方式">
                    {retentions.map((item) => (
                      <button
                        key={item.value}
                        type="button"
                        role="radio"
                        aria-checked={draft.retention === item.value}
                        className={draft.retention === item.value ? "active" : ""}
                        disabled={busy}
                        onClick={() => update({ retention: item.value })}
                      >
                        {item.label}
                      </button>
                    ))}
                  </div>
                  {draft.retention === "count" ? (
                    <div className="bk-keep">
                      <Stepper
                        value={draft.keep}
                        min={1}
                        max={maxKeep}
                        label="保留份数"
                        disabled={busy}
                        onChange={(keep) => update({ keep })}
                      />
                      <span>份定时快照</span>
                    </div>
                  ) : (
                    <TierEditor
                      frequency={draft.frequency}
                      tiers={draft.tiers}
                      disabled={busy}
                      onChange={(tiers) => update({ tiers })}
                    />
                  )}
                </>
              )}
              <RetentionStrip settings={draft} />
              <small className="bk-retention-note">{describeRetention(draft)}</small>
            </div>
          </div>
        </div>
      ) : (
        <p className="bk-schedule-off">
          {readOnly
            ? "管理员尚未为共享空间开启定时快照；开启后你可以浏览和恢复其中的文件。"
            : "开启后每天自动拍一份快照，误删、改坏的文件都能找回。"}
        </p>
      )}

      <ScheduleStatus schedule={schedule} offline={Boolean(data[space.id]?.offline)} />

      {readOnly && draft.enabled && (
        <p className="bk-readonly-note">共享空间的定时快照由管理员设置，你可以浏览和恢复其中的文件。</p>
      )}
      {dirty && !readOnly && (
        <footer className="bk-save-bar">
          <span>
            {draft.enabled
              ? `将${describeSchedule(draft)} 自动拍一份，${describeRetention(draft)}`
              : "将关闭定时快照，已有快照会保留"}
          </span>
          <button
            type="button"
            className="bk-secondary small"
            disabled={saving}
            onClick={() => {
              setDraft(stored);
              setCustom(!planOf(stored));
            }}
          >
            还原
          </button>
          <button type="button" className="bk-primary small" disabled={saving} onClick={() => void save()}>
            {saving ? (
              <>
                <Spinner small />
                保存中…
              </>
            ) : (
              "保存"
            )}
          </button>
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
      {schedule.enabled && schedule.nextRunAt && (
        <span>
          <CalendarClock />
          下次：{formatUntil(schedule.nextRunAt)}
          {offline ? "（数据卷离线）" : ""}
        </span>
      )}
      {run &&
        (run.status === "succeeded" ? (
          <span className="ok">
            <CircleCheck />
            上次：{formatDateTime(run.at)} 成功
          </span>
        ) : (
          <span className="failed">
            <CircleAlert />
            上次：{formatDateTime(run.at)} 失败，{runErrorText(schedule)}
          </span>
        ))}
      {!schedule.enabled && (
        <span>
          <Clock3 />
          已关闭
        </span>
      )}
    </div>
  );
}

// TierEditor sets how many hours, days, weeks and months keep a snapshot;
// tiers finer than the frequency do not apply.
function TierEditor({
  frequency,
  tiers,
  disabled,
  onChange,
}: {
  frequency: Frequency;
  tiers: RetentionTiers;
  disabled: boolean;
  onChange: (tiers: RetentionTiers) => void;
}) {
  const total = tierTotal(tiers);
  return (
    <div className="bk-tiers" role="group" aria-label="智能保留">
      {tiersFor(frequency).map((tier) => (
        <div key={tier} className="bk-tier">
          <span className="bk-tier-name">{tierLabel[tier].every}一份</span>
          <Stepper
            value={tiers[tier]}
            min={0}
            max={Math.min(maxKeep, tiers[tier] + maxKeep - total)}
            disabled={disabled}
            label={`保留最近几${tierLabel[tier].span}`}
            onChange={(value) => onChange({ ...tiers, [tier]: value })}
          />
          <span className="bk-tier-span">{tiers[tier] ? `最近 ${tiers[tier]} ${tierLabel[tier].span}` : "不保留"}</span>
        </div>
      ))}
    </div>
  );
}

// The strip's axis: equal parts for the last day, week, month and year, each
// linear inside, so a retention's thinning out shows at every distance.
const zones: { until: number; label: string }[] = [
  { until: 24, label: "1 天" },
  { until: 168, label: "1 周" },
  { until: 730, label: "1 个月" },
  { until: 8760, label: "1 年" },
];
const tierHours: Record<Tier, number> = { hourly: 1, daily: 24, weekly: 168, monthly: 730 };
const frequencyHours: Record<Frequency, number> = { hourly: 1, daily: 24, weekly: 168 };

// restorePoints estimates the ages, in hours, of the scheduled snapshots a
// retention keeps when the device runs without a break.
function restorePoints(settings: Pick<ScheduleSettings, "frequency" | "keep" | "retention" | "tiers">): number[] {
  const step = frequencyHours[settings.frequency];
  if (settings.retention !== "tiered") return Array.from({ length: settings.keep }, (_, index) => (index + 1) * step);
  const ages: number[] = [];
  let reached = 0;
  for (const tier of tiersFor(settings.frequency)) {
    const every = tierHours[tier];
    for (let index = 1; index <= settings.tiers[tier]; index++) {
      const age = index * every;
      if (age > reached) ages.push(age);
    }
    reached = Math.max(reached, settings.tiers[tier] * every);
  }
  return ages;
}

// RetentionStrip draws the restore points from a year ago, on the left, to
// now. The axis stays put while settings change, so the past a retention
// cannot reach shows as empty track.
function RetentionStrip({ settings }: { settings: ScheduleSettings }) {
  const ages = restorePoints(settings);
  if (!ages.length) return null;
  const oldest = Math.max(...ages);
  const bounds = [0, ...zones.slice(0, -1).map((zone) => zone.until), Math.max(zones.at(-1)!.until, oldest)];
  const share = 100 / zones.length;
  const position = (age: number) => {
    const zone = Math.max(0, bounds.findIndex((bound, index) => index > 0 && age <= bound) - 1);
    return 100 - (zone + (age - bounds[zone]) / (bounds[zone + 1] - bounds[zone])) * share;
  };
  return (
    <div
      className="bk-strip-retention"
      role="img"
      aria-label={`保留${settings.retention === "tiered" ? "约" : ""} ${ages.length} 份定时快照，最早约 ${describeAge(oldest)}前`}
    >
      <div className="bk-retention-track">
        <span className="bk-retention-reach" style={{ left: `${position(oldest)}%` }} />
        {zones.slice(0, -1).map((zone) => (
          <b key={zone.until} style={{ left: `${position(zone.until)}%` }} />
        ))}
        {ages.map((age) => (
          <i key={age} style={{ left: `${position(age)}%` }} />
        ))}
      </div>
      <div className="bk-retention-axis">
        <span className="far">{describeAge(bounds.at(-1)!)}前</span>
        {zones.slice(0, -1).map((zone) => (
          <span key={zone.until} style={{ left: `${position(zone.until)}%` }}>
            {zone.label}
          </span>
        ))}
        <span className="now">现在</span>
      </div>
    </div>
  );
}

function describeAge(hours: number): string {
  if (hours < 48) return `${hours} 小时`;
  if (hours < 24 * 60) return `${Math.round(hours / 24)} 天`;
  if (hours < 24 * 335) return `${Math.round(hours / 730)} 个月`;
  return `${Math.round((hours / 8760) * 10) / 10} 年`;
}
