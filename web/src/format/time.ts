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
