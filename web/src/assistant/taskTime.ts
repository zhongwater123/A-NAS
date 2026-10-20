// Times of scheduled tasks as the window and the desktop's toasts say them;
// kept apart from taskSchedule so the desktop does not load cronstrue.

/** A time near now as a family member says it: 今天 08:00, 明天 08:00, 10月25日 08:00. */
export function whenText(seconds: number, now = new Date()): string {
  const date = new Date(seconds * 1000);
  const time = `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  const days = Math.round((startOfDay(date) - startOfDay(now)) / 86_400_000);
  if (days === 0) return `今天 ${time}`;
  if (days === 1) return `明天 ${time}`;
  if (days === -1) return `昨天 ${time}`;
  const day = `${date.getMonth() + 1}月${date.getDate()}日`;
  return `${date.getFullYear() === now.getFullYear() ? day : `${date.getFullYear()}年${day}`} ${time}`;
}

function startOfDay(date: Date): number {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}
