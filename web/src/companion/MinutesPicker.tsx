import { useEffect, useState } from "react";

import type { FileEntry, Space } from "../api.generated";
import { failureText } from "../assistant/api";
import { listEntries, listSpaces } from "../files/api";
import styles from "./companion.module.css";

export interface MinutesSource {
  id: string;
  path: string;
  name: string;
}
export function MinutesPicker({
  userId,
  disabled,
  onSelect,
}: {
  userId: string;
  disabled: boolean;
  onSelect(source: MinutesSource): void;
}) {
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [spaceId, setSpaceId] = useState("");
  const [folders, setFolders] = useState<FileEntry[]>([]);
  const [listing, setListing] = useState<{ key: string; entries: FileEntry[]; error: string }>({
    key: "",
    entries: [],
    error: "",
  });
  const [spaceError, setError] = useState("");
  const [spaceLoading, setLoading] = useState(true);
  useEffect(() => {
    let active = true;
    listSpaces()
      .then((all) => {
        if (!active) return;
        const visible = all.filter((s) => !s.viewing && (s.kind === "shared" || s.ownerUserId === userId));
        setSpaces(visible);
        setSpaceId(visible[0]?.id ?? "");
        setLoading(false);
      })
      .catch((e) => {
        if (active) {
          setError(failureText(e));
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, [userId]);
  const parentId = folders.at(-1)?.id ?? "";
  const listingKey = `${spaceId}:${parentId}`;
  const loading = spaceLoading || (Boolean(spaceId) && listing.key !== listingKey);
  const entries = loading ? [] : listing.entries;
  const error = spaceError || (listing.key === listingKey ? listing.error : "");
  useEffect(() => {
    if (!spaceId) return;
    let active = true;
    listEntries(spaceId, parentId)
      .then((all) => {
        if (active) setListing({ key: listingKey, entries: all, error: "" });
      })
      .catch((e) => {
        if (active) setListing({ key: listingKey, entries: [], error: failureText(e) });
      });
    return () => {
      active = false;
    };
  }, [spaceId, parentId, listingKey]);
  const space = spaces.find((s) => s.id === spaceId);
  const safeName = (name: string) => name !== "." && name !== ".." && !/[\\/]/.test(name) && !name.includes("\0");
  return (
    <fieldset className={styles.picker} disabled={disabled}>
      <legend>选一份会议记录</legend>
      <label>
        NAS 空间{" "}
        <select
          value={spaceId}
          onChange={(e) => {
            setFolders([]);
            setSpaceId(e.target.value);
          }}
        >
          {spaces.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      </label>
      <div className={styles.breadcrumb}>
        <button type="button" onClick={() => setFolders([])}>
          根目录
        </button>
        {folders.map((f, i) => (
          <button type="button" key={f.id} onClick={() => setFolders(folders.slice(0, i + 1))}>
            {f.name}
          </button>
        ))}
      </div>
      <p className={styles.note}>先支持 32 KiB 以内的 .txt / .md，文件由你的 VNAS 读取。</p>
      {loading && <p role="status">正在读取文件列表…</p>}
      {error && <p role="alert">{error}</p>}
      {!loading && !error && entries.length === 0 && <p>这里还没有文件。</p>}
      <ul className={styles.fileList}>
        {entries.map((entry) => {
          const usable =
            safeName(entry.name) &&
            (entry.kind === "directory" || (/\.(txt|md)$/i.test(entry.name) && entry.sizeBytes <= 32 * 1024));
          return (
            <li key={entry.id}>
              <button
                type="button"
                disabled={!usable}
                onClick={() => {
                  if (entry.kind === "directory") {
                    setFolders([...folders, entry]);
                    return;
                  }
                  if (!space || !folders.every((f) => safeName(f.name))) return;
                  const path = [
                    "/nas",
                    space.kind === "private" ? "个人空间" : "共享空间",
                    ...folders.map((f) => f.name),
                    entry.name,
                  ].join("/");
                  onSelect({ id: entry.id, name: entry.name, path });
                }}
              >
                {entry.kind === "directory" ? "文件夹 · " : ""}
                {entry.name}
              </button>
            </li>
          );
        })}
      </ul>
    </fieldset>
  );
}
