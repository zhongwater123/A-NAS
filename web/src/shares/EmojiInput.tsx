import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { Search, Smile, X } from "lucide-react";
import { allEmoji, emojiGroups } from "./emoji";

const recentKey = "anas:shares:recent-emoji";
function recentEmoji(): string[] {
  try { const value: unknown = JSON.parse(localStorage.getItem(recentKey) ?? "[]"); return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string" && allEmoji.some((entry) => entry.emoji === item)).slice(0, 24) : []; }
  catch { return []; }
}
export function EmojiInput({ label, value, onChange, disabled = false }: { label: string; value: string; onChange: (value: string) => void; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const [category, setCategory] = useState("常用");
  const [query, setQuery] = useState("");
  const [recent, setRecent] = useState(recentEmoji);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const cursor = useRef<number | undefined>(undefined);
  const id = useId();
  useLayoutEffect(() => { if (cursor.current !== undefined && input.current) { input.current.focus(); input.current.setSelectionRange(cursor.current, cursor.current); cursor.current = undefined; } }, [value]);
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  const close = () => { setOpen(false); trigger.current?.focus(); };
  const insert = (emoji: string) => {
    if (disabled) return;
    const start = input.current?.selectionStart ?? value.length, end = input.current?.selectionEnd ?? value.length;
    const next = value.slice(0, start) + emoji + value.slice(end);
    if (next.length > 8000) return;
    cursor.current = start + emoji.length; onChange(next);
    const updated = [emoji, ...recent.filter((item) => item !== emoji)].slice(0, 24);
    setRecent(updated);
    try { localStorage.setItem(recentKey, JSON.stringify(updated)); } catch { /* Preferences are optional. */ }
  };
  const filtered = query.trim() ? allEmoji.filter((item) => `${item.emoji} ${item.name}`.includes(query.trim().toLowerCase()))
    : category === "最近使用" ? recent.map((emoji) => allEmoji.find((item) => item.emoji === emoji)!).filter(Boolean)
      : emojiGroups.find((group) => group.name === category)!.items;
  return <div ref={root} className="share-text-input" onKeyDown={(event) => { if (open && event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(); } }}>
    <label>{label}<textarea ref={input} aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} maxLength={8000} rows={3} placeholder="输入文字或表情…" /></label>
    <button ref={trigger} type="button" aria-label="选择表情" aria-controls={id} aria-expanded={open} disabled={disabled} onClick={() => { setOpen(!open); setQuery(""); }}><Smile />表情</button>
    {open && <div id={id} className="share-emoji-picker" role="dialog" aria-label="表情">
      <div className="share-emoji-heading"><strong>表情</strong><button type="button" aria-label="关闭表情" onClick={close}><X /></button></div>
      <label className="share-emoji-search"><Search /><input type="search" aria-label="搜索表情" onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); event.stopPropagation(); } }} placeholder="搜索：开心、庆祝、猫…" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
      <nav aria-label="表情分类"><button type="button" aria-label="最近使用" title="最近使用" aria-pressed={!query && category === "最近使用"} onClick={() => { setCategory("最近使用"); setQuery(""); }}>◷</button>{emojiGroups.map((group) => <button type="button" key={group.name} aria-label={group.name} title={group.name} aria-pressed={!query && category === group.name} onClick={() => { setCategory(group.name); setQuery(""); }}>{group.icon}</button>)}</nav>
      <p className="share-emoji-caption">{query ? `搜索结果 · ${filtered.length}` : category}</p>
      <div className="share-emoji-grid">{filtered.map(({ emoji, name }) => <button type="button" key={emoji} aria-label={emoji} title={name} onClick={() => insert(emoji)}>{emoji}</button>)}</div>
      {!filtered.length && <p className="share-emoji-empty">{query ? "没有找到表情，试试其他关键词" : "使用过的表情会出现在这里"}</p>}
    </div>}
  </div>;
}