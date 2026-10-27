import { Search, X } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import styles from "../settings.module.css";

// Filters outlive the settings window until the page reloads, like the
// settings section itself.
const remembered = new Map<string, unknown>();

export function useRemembered<T>(key: string, initial: T): [T, (next: T | ((current: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => (remembered.has(key) ? (remembered.get(key) as T) : initial));
  const update = useCallback(
    (next: T | ((current: T) => T)) => {
      setValue((current) => {
        const resolved = typeof next === "function" ? (next as (current: T) => T)(current) : next;
        remembered.set(key, resolved);
        return resolved;
      });
    },
    [key],
  );
  return [value, update];
}

export function forgetRememberedFilters() {
  remembered.clear();
}

// useDebounced settles typed search text before it becomes a request.
export function useDebounced<T>(value: T, delay = 300): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);
  return settled;
}

export function SearchField({
  value,
  onChange,
  label,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
  placeholder: string;
}) {
  return (
    <label className={styles.lgSearch}>
      <Search aria-hidden="true" />
      <input
        type="search"
        aria-label={label}
        placeholder={placeholder}
        value={value}
        maxLength={100}
        onChange={(event) => onChange(event.target.value)}
      />
      {value && (
        <button type="button" className={styles.lgSearchClear} aria-label="清除搜索" onClick={() => onChange("")}>
          <X />
        </button>
      )}
    </label>
  );
}

export function Select<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: Array<{ id: T; label: string }>;
  onChange: (value: T) => void;
}) {
  return (
    <select
      className={styles.lgSelect}
      aria-label={label}
      value={value}
      onChange={(event) => onChange(event.target.value as T)}
    >
      {options.map((option) => (
        <option key={option.id} value={option.id}>
          {option.label}
        </option>
      ))}
    </select>
  );
}

export const exportLimit = 5000;

// downloadText hands the viewer a file without a server round trip.
export function downloadText(filename: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.hidden = true;
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// toCSV quotes every cell and starts with a byte order mark so that Excel
// reads the Chinese text as UTF-8. Nicknames and file names are chosen by
// members, so a cell that a spreadsheet would run as a formula gets a leading
// apostrophe and stays text.
export function toCSV(rows: string[][]): string {
  const text = (cell: string) => (/^[=+\-@\t\r]/.test(cell) ? `'${cell}` : cell);
  return (
    "﻿" + rows.map((row) => row.map((cell) => `"${text(cell).replaceAll('"', '""')}"`).join(",")).join("\r\n") + "\r\n"
  );
}

export function fileStamp(now = new Date()): string {
  const two = (value: number) => String(value).padStart(2, "0");
  return `${now.getFullYear()}${two(now.getMonth() + 1)}${two(now.getDate())}-${two(now.getHours())}${two(now.getMinutes())}`;
}
