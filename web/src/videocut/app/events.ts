// useWorkEvents: what happens once when a project's state changes, by the
// state before and after (only the shell mounts it, once): the film made and
// the files analysis left out, the export done and its download, the export
// failed with a retry, an interrupted upload, and queueing.
import { useEffect, useRef } from "react";

import { exportFilm, filmURL, type Project } from "../api/client";
import { useBanner } from "../ui/Banner";
import { messages } from "../ui/messages";
import { useToast } from "../ui/Toast";

const making = new Set<Project["state"]>(["uploading", "queued", "generating"]);

// download starts the exported film's download; a browser that blocks it
// leaves the banner's link.
function download(work: Project): boolean {
  const link = document.createElement("a");
  link.href = filmURL(work.id);
  link.download = "";
  try {
    link.click();
    return true;
  } catch {
    return false;
  }
}

export function useWorkEvents(work: Project | undefined) {
  const toast = useToast();
  const banner = useBanner();
  const before = useRef<Project | undefined>(undefined);
  useEffect(() => {
    if (!work) return;
    const last = before.current?.id === work.id ? before.current : undefined;
    before.current = work;

    if (work.state === "upload_interrupted") banner.show({ key: "upload", text: messages.uploadInterrupted });
    else banner.hide("upload");
    if (work.state === "queued") banner.show({ key: "queue", text: messages.queued });
    else banner.hide("queue");

    if (!last || last.state === work.state) return;
    if (making.has(last.state) && work.state === "ready") {
      toast(messages.generated);
      if (work.progress.skipped > 0) toast(messages.skipped(work.progress.skipped));
    }
    if (last.state === "exporting" && work.state === "exported") {
      if (download(work)) toast(messages.exported);
      else
        banner.show({
          key: "download",
          text: messages.downloadBlocked,
          action: { label: messages.download, run: () => download(work) },
        });
    }
    if (last.state === "exporting" && work.state === "ready" && work.exportFailure) {
      const reason = messages.failure[work.exportFailure.reason];
      if (work.exportFailure.retries >= 3) toast(`${messages.exportFailed}：${messages.tooManyRetries}`);
      else toast(`${messages.exportFailed}：${reason}`, { label: messages.retry, run: () => void exportFilm(work.id) });
    }
  }, [work, toast, banner]);
}
