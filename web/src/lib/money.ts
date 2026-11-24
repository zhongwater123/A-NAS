// Amounts are renminbi kept in whole fen (分), so no sum is ever off by a
// rounding of floating point; people read and type them in yuan.
const yuan = new Intl.NumberFormat("zh-CN", { style: "currency", currency: "CNY" });

// formatYuan shows fen as yuan: 12850 → ¥128.50.
export function formatYuan(cents: number): string {
  if (!Number.isSafeInteger(cents)) return "—";
  return yuan.format(cents / 100);
}

// parseYuan reads what someone typed in yuan (128, 128.5, ¥1,280.00) as fen;
// undefined when it is not an amount of at most two decimals.
export function parseYuan(text: string): number | undefined {
  const plain = text
    .trim()
    .replace(/^[¥￥]\s*/, "")
    .replace(/[,，]/g, "");
  const match = /^(\d{1,12})(?:\.(\d{0,2}))?$/.exec(plain);
  if (!match) return undefined;
  return Number(match[1]) * 100 + Number((match[2] ?? "").padEnd(2, "0"));
}

// yuanText is fen as people type them back: 12850 → 128.50, 300 → 3.
export function yuanText(cents: number): string {
  const whole = Math.floor(cents / 100);
  const rest = cents % 100;
  return rest ? `${whole}.${String(rest).padStart(2, "0")}` : String(whole);
}
