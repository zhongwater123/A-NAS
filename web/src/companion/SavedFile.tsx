import { useEffect, useState } from "react";

import { failureText } from "../assistant/api";
import { listEntries, listSpaces, readFileTextPreview } from "../files/api";
import styles from "./companion.module.css";

// Resolve an executor-confirmed path back to a real file ID, then use the
// existing authenticated preview API. Never turn model prose into a URL.
export function SavedFile({ path, userId }: { path: string; userId: string }) {
  const [text, setText] = useState<string>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!attempt) return;
    let active = true;
    const load = async () => {
      if (!path.startsWith("/nas/个人空间/")) throw new Error("请在 VNAS 中查看这个位置");
      const names = path.slice("/nas/个人空间/".length).split("/");
      if (names.some((n) => !n || n === "." || n === ".." || n.includes("\\") || n.includes("\0")))
        throw new Error("文件位置无效");
      const own = (await listSpaces()).find((s) => s.kind === "private" && s.ownerUserId === userId && !s.viewing);
      if (!own) throw new Error("找不到你的个人空间");
      let parent = "";
      for (const [i, name] of names.entries()) {
        const entry = (await listEntries(own.id, parent)).find((e) => e.name === name);
        if (!entry) throw new Error("文件可能已移动，请在文件管理中查找");
        if (i < names.length - 1) {
          if (entry.kind !== "directory") throw new Error("文件位置已变化");
          parent = entry.id;
        } else {
          if (entry.kind !== "file" || !/\.(txt|md)$/i.test(entry.name) || entry.sizeBytes > 256 * 1024)
            throw new Error("请在文件管理中打开这个文件");
          return readFileTextPreview(entry.id);
        }
      }
      return "";
    };
    load()
      .then((value) => {
        if (active) setText(value);
      })
      .catch((e) => {
        if (active) setError(e instanceof Error ? e.message : failureText(e));
      })
      .finally(() => {
        if (active) setBusy(false);
      });
    return () => {
      active = false;
    };
  }, [path, userId, attempt]);
  return (
    <li className={styles.result}>
      {path}{" "}
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          setError("");
          setAttempt((n) => n + 1);
        }}
      >
        {busy ? "正在读取…" : "打开文件预览"}
      </button>
      {error && <p role="alert">{error}</p>}
      {text !== undefined && <pre>{text}</pre>}
    </li>
  );
}
