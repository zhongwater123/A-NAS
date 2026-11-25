// Dates are local calendar dates; durations use elapsed milliseconds. Keep
// that distinction across midnight and daylight-saving changes.
export type TimeValue = string | number | Date;
const minute = 60_000;
const day = 86_400_000;
const dateFormats = new Map<string, Intl.DateTimeFormat>();
const relativeFormats = {
  always: new Intl.RelativeTimeFormat("zh-CN", { numeric: "always" }),
  auto: new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" }),
};

export function formatDate(value: TimeValue, options: Intl.DateTimeFormatOptions = {}): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "—";
  const key = JSON.stringify(options);
  let formatter = dateFormats.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("zh-CN", options);
    dateFormats.set(key, formatter);
  }
  return formatter.format(date);
}

export function formatClock(value: TimeValue, seconds = false): string {
  return formatDate(value, {
    hour: "2-digit",
    minute: "2-digit",
    ...(seconds ? { second: "2-digit" } : {}),
    hourCycle: "h23",
  });
}

export function calendarDays(value: TimeValue, now: TimeValue = Date.now()): number {
  const ordinal = (input: TimeValue) => {
    const date = new Date(input);
    return Date.UTC(date.getFullYear(), date.getMonth(), date.getDate());
  };
  return (ordinal(value) - ordinal(now)) / day;
}

// Date-only values (YYYY-MM-DD) name a local calendar day. new Date() would
// read them as midnight UTC, a day early west of Greenwich.
export function dateOnly(value: string): Date {
  const [year, month, dayOfMonth] = value.split("-").map(Number);
  return new Date(year, month - 1, dayOfMonth);
}

// isoDate is the local calendar day of a moment as YYYY-MM-DD.
export function isoDate(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

export function today(): string {
  return isoDate(new Date());
}

// daysUntil counts calendar days from today to a date-only value: 1 tomorrow,
// 0 today, -1 yesterday.
export function daysUntil(value: string, now: TimeValue = Date.now()): number {
  return calendarDays(dateOnly(value), now);
}

// dueText says how far off a date-only due date is: “还有 3 天”, “今天截止” or
// “已过期 2 天”, short enough to sit beside a title of ten characters.
export function dueText(due: string, now: TimeValue = Date.now()): string {
  const days = daysUntil(due, now);
  if (days > 0) return `还有 ${days} 天`;
  if (days === 0) return "今天截止";
  return `已过期 ${-days} 天`;
}

// dayLabel is a date-only value as “11月20日”, with the year when it is not
// this year's.
export function dayLabel(value: string, now: TimeValue = Date.now()): string {
  return calendarDate(dateOnly(value), now);
}

// untilNextDay is how long it is from now until a little after the next local
// midnight, when date-only values move a day closer.
export function untilNextDay(now: TimeValue = Date.now(), after = minute): number {
  const date = new Date(now);
  const midnight = new Date(date.getFullYear(), date.getMonth(), date.getDate() + 1);
  return midnight.getTime() - date.getTime() + after;
}

export function calendarDate(value: TimeValue, now: TimeValue = Date.now(), weekday?: "short" | "long"): string {
  const date = new Date(value);
  const label = formatDate(date, {
    ...(date.getFullYear() !== new Date(now).getFullYear() ? { year: "numeric" } : {}),
    month: "long",
    day: "numeric",
  });
  if (label === "—") return label;
  return weekday ? `${label} ${formatDate(date, { weekday })}` : label;
}

// Spacing is a product typography choice; Intl supplies every unit and tense.
export function relativeUnit(
  value: number,
  unit: Intl.RelativeTimeFormatUnit,
  numeric: "auto" | "always" = "always",
): string {
  if (!Number.isFinite(value)) return "—";
  return relativeFormats[numeric]
    .formatToParts(value, unit)
    .map(
      (part, index, parts) => part.value + (part.type === "integer" && parts[index + 1]?.type === "literal" ? " " : ""),
    )
    .join("");
}

export function calendarLabel(value: TimeValue, now: TimeValue = Date.now(), weekday?: "short" | "long"): string {
  const days = calendarDays(value, now);
  return Math.abs(days) <= 1 ? relativeUnit(days, "day", "auto") : calendarDate(value, now, weekday);
}

export function calendarMoment(value: TimeValue, now: TimeValue = Date.now()): string {
  if (!Number.isFinite(new Date(value).getTime())) return "—";
  return `${calendarLabel(value, now)} ${formatClock(value)}`;
}

export function relativeTime(
  value: TimeValue,
  now: TimeValue = Date.now(),
  options: {
    dateAfterDays?: number;
    withTime?: boolean;
  } = {},
): string {
  const elapsed = new Date(value).getTime() - new Date(now).getTime();
  if (!Number.isFinite(elapsed)) return "—";
  if (Math.abs(elapsed) < minute) return "刚刚";
  if (Math.abs(elapsed) >= (options.dateAfterDays ?? 30) * day) {
    return options.withTime ? calendarMoment(value, now) : calendarDate(value, now);
  }
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ...(options.dateAfterDays === Infinity
      ? ([
          ["year", 365 * day],
          ["month", 30 * day],
          ["week", 7 * day],
        ] as [Intl.RelativeTimeFormatUnit, number][])
      : []),
    ["day", day],
    ["hour", 60 * minute],
    ["minute", minute],
  ];
  for (const [unit, size] of steps) {
    // Elapsed units are not calendar offsets: 48 hours can span three midnights.
    if (Math.abs(elapsed) >= size) return relativeUnit(Math.trunc(elapsed / size), unit);
  }
  return "刚刚";
}
