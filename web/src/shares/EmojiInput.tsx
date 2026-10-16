import { useState } from "react";

export function EmojiInput({ label, value, onChange, disabled = false }: { label: string; value: string; onChange: (value: string) => void; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  return <div className="share-text-input"><label>{label}<textarea aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} maxLength={8000} rows={3} placeholder="输入文字、换行或表情…" /></label><button type="button" aria-label="选择表情" aria-expanded={open} disabled={disabled} onClick={() => setOpen(!open)}>☺ 表情</button>{open && <div className="share-emoji" aria-label="表情">{["😀", "😊", "👍", "❤️", "🎉", "🙏", "👀", "✅", "👨‍👩‍👧‍👦", "🌈"].map((emoji) => <button type="button" key={emoji} aria-label={emoji} onClick={() => { onChange(value + emoji); setOpen(false); }}>{emoji}</button>)}</div>}</div>;
}
