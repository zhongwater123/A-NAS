import {
  Activity,
  ChevronDown,
  Download,
  FolderOpen,
  HardDrive,
  Images,
  LogIn,
  type LucideIcon,
  RefreshCw,
  ScrollText,
  ShieldAlert,
  UserCog,
} from "lucide-react";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { Session, User } from "../../api.generated";
import { displayName, fullName } from "../../people/people";
import { listUsers } from "../api";
import { Banner, messageOf } from "../parts";
import styles from "../settings.module.css";
import {
  type ActivityCategory,
  type ActivityEvent,
  type ActivityFilters,
  listActivity,
  type TimeRange,
  timeRanges,
} from "./api";
import {
  downloadText,
  exportLimit,
  fileStamp,
  SearchField,
  Select,
  toCSV,
  useDebounced,
  useRemembered,
} from "./common";
import {
  type ActivityGroup,
  categoryInfo,
  clockTime,
  type Described,
  dayLabel,
  describeActivity,
  describeGroup,
  fullTime,
  groupActivity,
  originLabel,
  type Part,
  plainText,
} from "./describe";

export interface LogsPreset {
  category: ActivityCategory;
  id: number;
}

const categoryIcons: Record<ActivityCategory | "other", LucideIcon> = {
  session: LogIn,
  account: UserCog,
  file: FolderOpen,
  storage: HardDrive,
  photo: Images,
  other: Activity,
};

const categoryOrder: ActivityCategory[] = ["session", "account", "file", "storage", "photo"];

const pageSize = 50;

const unavailableLabels: Record<ActivityCategory, string> = {
  session: "登录",
  account: "账号",
  file: "文件",
  storage: "存储",
  photo: "相册",
};

interface LogState {
  items: ActivityEvent[];
  next?: string;
  unavailable: ActivityCategory[];
  loading: boolean;
  loaded: boolean;
  error: string;
}

export function ActivityLog({ session, preset }: { session: Session; preset?: LogsPreset }) {
  const viewerId = session.user.id;
  const isAdmin = session.user.role === "admin";
  const [filters, setFilters] = useRemembered<ActivityFilters>(`${viewerId}:activity`, { range: "all", q: "" });
  const [text, setText] = useState(filters.q);
  const settledText = useDebounced(text);
  const [state, setState] = useState<LogState>({ items: [], unavailable: [], loading: true, loaded: false, error: "" });
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [users, setUsers] = useState<User[]>([]);
  const [exporting, setExporting] = useState<{ count: number } | undefined>();
  const [notice, setNotice] = useState("");
  const controller = useRef<AbortController | undefined>(undefined);
  const sentinel = useRef<HTMLDivElement>(null);

  // A preset applies once, not every time the section is shown again.
  const [appliedPreset, setAppliedPreset] = useRemembered(`${viewerId}:activity-preset`, 0);
  useEffect(() => {
    if (!preset || preset.id === appliedPreset) return;
    setAppliedPreset(preset.id);
    setText("");
    setFilters((current) => ({ ...current, category: preset.category, actor: undefined, q: "" }));
  }, [preset, appliedPreset, setAppliedPreset, setFilters]);
  useEffect(() => {
    setFilters((current) => (current.q === settledText ? current : { ...current, q: settledText }));
  }, [settledText, setFilters]);
  useEffect(() => {
    if (isAdmin)
      void listUsers()
        .then(setUsers)
        .catch(() => setUsers([]));
  }, [isAdmin]);

  const load = useCallback(
    async (before?: string) => {
      controller.current?.abort();
      const abort = new AbortController();
      controller.current = abort;
      setState((current) => ({ ...current, loading: true, error: "" }));
      try {
        const page = await listActivity(filters, before, pageSize, abort.signal);
        setState((current) => ({
          items: before ? [...current.items, ...page.items] : page.items,
          next: page.next,
          unavailable: page.unavailable ?? [],
          loading: false,
          loaded: true,
          error: "",
        }));
        if (!before) setExpanded(new Set());
      } catch (caught) {
        if (caught instanceof DOMException && caught.name === "AbortError") return;
        setState((current) => ({ ...current, loading: false, loaded: true, error: messageOf(caught) }));
      }
    },
    [filters],
  );
  useEffect(() => {
    void load();
    return () => controller.current?.abort();
  }, [load]);

  // Scrolling to the end reads the next page, as the button does.
  const { next, loading } = state;
  useEffect(() => {
    const target = sentinel.current;
    if (!target || !next || loading || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) void load(next);
      },
      { rootMargin: "160px" },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [next, loading, load]);

  const groups = useMemo(() => groupActivity(state.items), [state.items]);
  const days = useMemo(() => {
    const result: Array<{ label: string; groups: ActivityGroup[]; count: number }> = [];
    for (const group of groups) {
      const label = dayLabel(new Date(group.events[0].occurredAt));
      const day = result.at(-1);
      if (day?.label === label) {
        day.groups.push(group);
        day.count += group.events.length;
      } else result.push({ label, groups: [group], count: group.events.length });
    }
    return result;
  }, [groups]);
  const filtered = Boolean(filters.category || filters.actor || filters.range !== "all" || filters.q);
  const toggle = (key: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const clear = () => {
    setText("");
    setFilters({ range: "all", q: "" });
  };

  const exportCSV = async () => {
    setNotice("");
    setExporting({ count: 0 });
    try {
      const rows: ActivityEvent[] = [];
      let before: string | undefined;
      do {
        const page = await listActivity(filters, before, 200);
        rows.push(...page.items);
        before = page.next;
        setExporting({ count: rows.length });
      } while (before && rows.length < exportLimit);
      const kept = rows.slice(0, exportLimit);
      const table = [
        ["时间", "类别", "成员", "描述", "来源", "操作代码", "对象类型", "对象 ID", "记录 ID"],
        ...kept.map((event) => {
          const described = describeActivity(event, viewerId);
          return [
            fullTime(event.occurredAt),
            categoryInfo[event.category].label,
            event.system ? "系统" : event.actor ? fullName(event.actor) : "",
            plainText(described.parts),
            described.meta.join(" · "),
            event.action,
            event.resourceType,
            event.resourceId ?? "",
            event.id,
          ];
        }),
      ];
      downloadText(`a-nas-activity-${fileStamp()}.csv`, toCSV(table), "text/csv;charset=utf-8");
      setNotice(
        before ? `只导出了最新的 ${exportLimit.toLocaleString("zh-CN")} 条记录。` : `已导出 ${kept.length} 条记录。`,
      );
    } catch (caught) {
      setNotice(`导出失败：${messageOf(caught)}`);
    } finally {
      setExporting(undefined);
    }
  };

  const actorOptions = [
    { id: "", label: "所有成员" },
    ...users.map((user) => ({
      id: user.id,
      label: user.id === viewerId ? `${displayName(user)}（你）` : displayName(user),
    })),
    { id: "system", label: "系统任务" },
  ];
  return (
    <div className={styles.lgPanel}>
      <div className={styles.lgToolbar}>
        <div className={styles.lgToolbarRow}>
          <div className={styles.lgChips} role="radiogroup" aria-label="记录类别">
            {[undefined, ...categoryOrder].map((category) => {
              const active = filters.category === category;
              const Icon = category ? categoryIcons[category] : ScrollText;
              return (
                <button
                  key={category ?? "all"}
                  role="radio"
                  aria-checked={active}
                  className={`${styles.lgChip} ${active ? styles.active : ""}`}
                  onClick={() => setFilters((current) => ({ ...current, category }))}
                >
                  <Icon />
                  {category ? categoryInfo[category].label : "全部"}
                </button>
              );
            })}
          </div>
          <span className={styles.lgControlsGap} />
          <button
            className={`${styles.setIconBtn} ${styles.bordered}`}
            aria-label="刷新活动记录"
            disabled={state.loading}
            onClick={() => void load()}
          >
            <RefreshCw className={state.loading ? "spinning" : ""} />
          </button>
          <button
            className={styles.setBtn}
            disabled={Boolean(exporting) || !state.items.length}
            onClick={() => void exportCSV()}
          >
            <Download />
            {exporting ? `正在导出 ${exporting.count} 条…` : "导出"}
          </button>
        </div>
        <div className={styles.lgControls}>
          {isAdmin && (
            <Select
              label="成员"
              value={filters.actor ?? ""}
              options={actorOptions}
              onChange={(actor) => setFilters((current) => ({ ...current, actor: actor || undefined }))}
            />
          )}
          <Select<TimeRange>
            label="时间范围"
            value={filters.range}
            options={timeRanges}
            onChange={(range) => setFilters((current) => ({ ...current, range }))}
          />
          <SearchField
            label="搜索活动记录"
            placeholder={isAdmin ? "文件名、成员或 IP" : "文件名或 IP"}
            value={text}
            onChange={setText}
          />
        </div>
      </div>
      {notice && (
        <Banner tone={notice.startsWith("导出失败") ? "error" : "success"} onClose={() => setNotice("")}>
          {notice}
        </Banner>
      )}
      {state.unavailable.length > 0 && (
        <Banner tone="warning">
          {state.unavailable.map((category) => unavailableLabels[category]).join("、")}
          的活动暂时无法读取，其余记录照常显示。
        </Banner>
      )}
      {state.error && <Banner tone="error">无法读取活动记录：{state.error}</Banner>}
      {!state.loaded ? (
        <SkeletonList />
      ) : days.length === 0 ? (
        !state.error && (
          <div className={`${styles.setCard} ${styles.lgEmpty}`}>
            <span className={styles.lgEmptyArt}>
              <ScrollText />
            </span>
            <h3>{filtered ? "没有符合条件的记录" : "还没有活动记录"}</h3>
            <p>{filtered ? "换一个类别、成员或时间范围试试。" : "登录、文件操作和账号变更都会记录在这里。"}</p>
            {filtered && (
              <button className={styles.setBtn} onClick={clear}>
                清除筛选
              </button>
            )}
          </div>
        )
      ) : (
        <div className={styles.lgDays}>
          {days.map((day) => (
            <section key={day.label} className={styles.lgDay} aria-label={day.label}>
              <h3 className={styles.lgDayHead}>
                <span>{day.label}</span>
                <small>{day.count} 条</small>
              </h3>
              <ol className={`${styles.setCard} ${styles.lgList}`}>
                {day.groups.map((group) => (
                  <ActivityRow
                    key={group.key}
                    group={group}
                    viewerId={viewerId}
                    expanded={expanded}
                    onToggle={toggle}
                  />
                ))}
              </ol>
            </section>
          ))}
        </div>
      )}
      {state.loaded && state.items.length > 0 && (
        <div className={styles.lgFoot} ref={sentinel}>
          {state.next ? (
            <button className={styles.setBtn} disabled={state.loading} onClick={() => void load(state.next)}>
              {state.loading ? "正在读取…" : "加载更早的记录"}
            </button>
          ) : (
            <span>没有更早的记录了</span>
          )}
        </div>
      )}
      {isAdmin && state.loaded && (
        <p className={styles.setFootnote}>
          其他成员个人空间中的文件名不会显示，相册中的操作只有本人能看到；需要找回内容时请在“用户与权限”中开启可审计的只读查看。
        </p>
      )}
    </div>
  );
}

function ActivityRow({
  group,
  viewerId,
  expanded,
  onToggle,
}: {
  group: ActivityGroup;
  viewerId: string;
  expanded: Set<string>;
  onToggle: (key: string) => void;
}) {
  const [first] = group.events;
  const last = group.events.at(-1)!;
  const described = describeGroup(group, viewerId);
  const open = expanded.has(group.key);
  const grouped = group.events.length > 1;
  return (
    <li className={`${styles.lgRow} ${styles[described.tone ?? ""] ?? ""} ${open ? styles.open : ""}`}>
      <button className={styles.lgRowMain} aria-expanded={open} onClick={() => onToggle(group.key)}>
        <CategoryTile category={first.category} described={described} />
        <span className={styles.lgRowText}>
          <Sentence parts={described.parts} />
          <small>{[categoryInfo[first.category].label, ...described.meta].join(" · ")}</small>
        </span>
        <time dateTime={first.occurredAt} title={fullTime(first.occurredAt)}>
          {grouped && clockTime(last.occurredAt) !== clockTime(first.occurredAt)
            ? `${clockTime(last.occurredAt)}–${clockTime(first.occurredAt)}`
            : clockTime(first.occurredAt)}
        </time>
        <ChevronDown className={styles.lgChevron} aria-hidden="true" />
      </button>
      {open &&
        (grouped ? (
          <ol className={styles.lgSub}>
            {group.events.map((event) => {
              const key = `${group.key}/${event.id}`;
              const itemOpen = expanded.has(key);
              const item = describeActivity(event, viewerId);
              return (
                <li key={event.id}>
                  <button className={styles.lgSubMain} aria-expanded={itemOpen} onClick={() => onToggle(key)}>
                    <time dateTime={event.occurredAt}>{clockTime(event.occurredAt, true)}</time>
                    <Sentence parts={item.parts.slice(1)} />
                    <ChevronDown className={styles.lgChevron} aria-hidden="true" />
                  </button>
                  {itemOpen && <EventDetails event={event} />}
                </li>
              );
            })}
          </ol>
        ) : (
          <EventDetails event={first} />
        ))}
    </li>
  );
}

function CategoryTile({ category, described }: { category: ActivityEvent["category"]; described: Described }) {
  const alert = described.tone === "warning" || described.tone === "danger";
  const Icon = alert ? ShieldAlert : categoryIcons[category];
  return (
    <span
      className={`${styles.lgCat} ${styles[categoryInfo[category].tone] ?? ""} ${styles[described.tone ?? ""] ?? ""}`}
      aria-hidden="true"
    >
      <Icon />
    </span>
  );
}

function Sentence({ parts }: { parts: Part[] }) {
  return (
    <span className={styles.lgSentence}>
      {parts.map((part, index) =>
        typeof part === "string" ? (
          <span key={index}>{part}</span>
        ) : (
          <strong key={index} className={part.muted ? styles.muted : ""}>
            {part.name}
          </strong>
        ),
      )}
    </span>
  );
}

function EventDetails({ event }: { event: ActivityEvent }) {
  const origin = event.category === "session" ? originLabel(event.detail) : undefined;
  const rows: Array<[string, ReactNode]> = [
    ["时间", fullTime(event.occurredAt)],
    ["操作代码", <code key="action">{event.action}</code>],
    [
      "对象",
      event.redacted ? (
        "其他成员个人空间中的内容，名称已隐藏"
      ) : (
        <>
          <span>{event.resourceType}</span>
          {event.resourceId && <code>{event.resourceId}</code>}
        </>
      ),
    ],
  ];
  if (origin) rows.push(["来源", origin]);
  if (event.detail && !event.redacted)
    rows.push([
      "详情",
      <code key="detail" className={styles.wrap}>
        {event.detail}
      </code>,
    ]);
  rows.push(["记录 ID", <code key="id">{event.id}</code>]);
  return (
    <dl className={styles.lgDetails}>
      {rows.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function SkeletonList() {
  return (
    <div className={`${styles.setCard} ${styles.lgSkeleton}`} role="status" aria-label="正在读取记录">
      {Array.from({ length: 6 }, (_, index) => (
        <div key={index}>
          <i />
          <span>
            <b style={{ width: `${46 + ((index * 17) % 38)}%` }} />
            <b />
          </span>
        </div>
      ))}
    </div>
  );
}
