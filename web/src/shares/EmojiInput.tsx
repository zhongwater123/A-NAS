import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Search, Smile, X } from "lucide-react";
import { allEmoji, emojiGroups } from "./emoji";

const recentKey = "anas:shares:recent-emoji";
function recentEmoji(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(recentKey) ?? "[]");
    return Array.isArray(value)
      ? value
          .filter((item): item is string => typeof item === "string" && allEmoji.some((entry) => entry.emoji === item))
          .slice(0, 24)
      : [];
  } catch {
    return [];
  }
}

// EmojiInput is the text field of the send dialog ("note") and of a private
// conversation ("chat"). In a chat Enter sends and Shift+Enter starts a new
// line; an IME composition's Enter only confirms the composition.
export function EmojiInput({
  label,
  value,
  onChange,
  disabled = false,
  variant = "note",
  placeholder = "输入文字或表情…",
  onEnter,
  trailing,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  variant?: "note" | "chat";
  placeholder?: string;
  onEnter?: () => void;
  trailing?: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [category, setCategory] = useState("常用");
  const [query, setQuery] = useState("");
  const [recent, setRecent] = useState(recentEmoji);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const cursor = useRef<number | undefined>(undefined);
  const id = useId();
  useLayoutEffect(() => {
    if (cursor.current !== undefined && input.current) {
      input.current.focus();
      input.current.setSelectionRange(cursor.current, cursor.current);
      cursor.current = undefined;
    }
  }, [value]);
  useLayoutEffect(() => {
    // A chat field grows with its text up to five lines, then scrolls.
    const field = input.current;
    if (variant !== "chat" || !field) return;
    field.style.height = "auto";
    field.style.height = `${Math.min(field.scrollHeight, 120)}px`;
  }, [value, variant]);
  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };
  const insert = (emoji: string) => {
    if (disabled) return;
    const start = input.current?.selectionStart ?? value.length,
      end = input.current?.selectionEnd ?? value.length;
    const next = value.slice(0, start) + emoji + value.slice(end);
    if (next.length > 8000) return;
    cursor.current = start + emoji.length;
    onChange(next);
    const updated = [emoji, ...recent.filter((item) => item !== emoji)].slice(0, 24);
    setRecent(updated);
    try {
      localStorage.setItem(recentKey, JSON.stringify(updated));
    } catch {
      /* Preferences are optional. */
    }
  };
  const filtered = query.trim()
    ? allEmoji.filter((item) => `${item.emoji} ${item.name}`.includes(query.trim().toLowerCase()))
    : category === "最近使用"
      ? recent.map((emoji) => allEmoji.find((item) => item.emoji === emoji)!).filter(Boolean)
      : emojiGroups.find((group) => group.name === category)!.items;
  return (
    <div
      ref={root}
      className={`sh-input sh-input-${variant}${disabled ? " disabled" : ""}`}
      onKeyDown={(event) => {
        if (open && event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          close();
        }
      }}
    >
      {variant === "note" && (
        <span className="sh-input-label" aria-hidden="true">
          {label}
          <small>可选</small>
        </span>
      )}
      <textarea
        ref={input}
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled}
        maxLength={8000}
        rows={variant === "chat" ? 1 : 3}
        placeholder={placeholder}
        onKeyDown={(event) => {
          if (
            !onEnter ||
            event.key !== "Enter" ||
            event.shiftKey ||
            event.nativeEvent.isComposing ||
            event.keyCode === 229
          )
            return;
          event.preventDefault();
          onEnter();
        }}
      />
      <div className="sh-input-tools">
        <button
          ref={trigger}
          type="button"
          className="sh-emoji-trigger"
          aria-label="选择表情"
          title="表情"
          aria-controls={id}
          aria-expanded={open}
          disabled={disabled}
          onClick={() => {
            setOpen(!open);
            setQuery("");
          }}
        >
          <Smile />
        </button>
        {trailing}
      </div>
      {open && (
        <div id={id} className="sh-emoji" role="dialog" aria-label="表情">
          <div className="sh-emoji-head">
            <label className="sh-emoji-search">
              <Search />
              <input
                type="search"
                aria-label="搜索表情"
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    event.stopPropagation();
                  }
                }}
                placeholder="搜索：开心、庆祝、猫…"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </label>
            <button type="button" className="sh-emoji-close" aria-label="关闭表情" onClick={close}>
              <X />
            </button>
          </div>
          <nav aria-label="表情分类">
            <button
              type="button"
              aria-label="最近使用"
              title="最近使用"
              aria-pressed={!query && category === "最近使用"}
              onClick={() => {
                setCategory("最近使用");
                setQuery("");
              }}
            >
              🕘
            </button>
            {emojiGroups.map((group) => (
              <button
                type="button"
                key={group.name}
                aria-label={group.name}
                title={group.name}
                aria-pressed={!query && category === group.name}
                onClick={() => {
                  setCategory(group.name);
                  setQuery("");
                }}
              >
                {group.icon}
              </button>
            ))}
          </nav>
          <p className="sh-emoji-caption">{query ? `搜索结果 · ${filtered.length}` : category}</p>
          <div className="sh-emoji-grid">
            {filtered.map(({ emoji, name }) => (
              <button type="button" key={emoji} aria-label={emoji} title={name} onClick={() => insert(emoji)}>
                {emoji}
              </button>
            ))}
          </div>
          {!filtered.length && (
            <p className="sh-emoji-empty">{query ? "没有找到表情，试试其他关键词" : "使用过的表情会出现在这里"}</p>
          )}
        </div>
      )}
    </div>
  );
}
