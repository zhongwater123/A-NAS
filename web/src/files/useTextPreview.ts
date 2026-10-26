import { useEffect, useState } from "react";

import { readFileTextPreview } from "../api";
import { isTextPreviewable } from "../fileTypes";
import { FileBrowserItem, messageOf } from "./model";

const textPreviewLimit = 1024 * 1024;

// useTextPreview reads the text of a text file selected alone; a large one
// says why it is not shown.
export function useTextPreview(item: FileBrowserItem | undefined, count: number) {
  const [textPreview, setTextPreview] = useState<{ id: string; content?: string; error?: string }>();
  const previewText = Boolean(item?.entry) && isTextPreviewable(item?.name ?? "") && count === 1;
  const previewTooLarge = previewText && (item?.sizeBytes ?? 0) > textPreviewLimit;
  const [previewFor, setPreviewFor] = useState<{
    id?: string;
    entry?: FileBrowserItem["entry"];
    name?: string;
    sizeBytes?: number;
    count: number;
  }>();
  if (
    !previewFor ||
    previewFor.id !== item?.id ||
    previewFor.entry !== item?.entry ||
    previewFor.name !== item?.name ||
    previewFor.sizeBytes !== item?.sizeBytes ||
    previewFor.count !== count
  ) {
    setPreviewFor({ id: item?.id, entry: item?.entry, name: item?.name, sizeBytes: item?.sizeBytes, count });
    setTextPreview(
      !previewText || !item
        ? undefined
        : previewTooLarge
          ? { id: item.id, error: "文本超过 1 MiB，请下载后查看完整内容。" }
          : { id: item.id },
    );
  }
  useEffect(() => {
    if (!item?.entry || !isTextPreviewable(item.name) || count !== 1) return;
    if (item.sizeBytes > textPreviewLimit) return;
    let cancelled = false;
    void readFileTextPreview(item.entry.id)
      .then((content) => {
        if (!cancelled) setTextPreview({ id: item.id, content });
      })
      .catch((caught) => {
        if (!cancelled) setTextPreview({ id: item.id, error: messageOf(caught) });
      });
    return () => {
      cancelled = true;
    };
  }, [item?.id, item?.entry, item?.name, item?.sizeBytes, count]);
  return textPreview;
}
