// useWorkEvents: what happens once when a project's state changes (only the
// shell mounts it, once). A finished export, a failed export and the files
// analysis left out are told once on this device, whether the page saw the
// work go on or opens the project later: "told" is remembered by the event's
// own time (exportedAt, exportFailure.at). Interrupted uploads and queueing
// show in the banner while they last, and the project's banners go when the
// member leaves it.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import { exportFilm, filmURL, type Project } from "../api/client";
import { hasFilm } from "../lib/pages";
import { useBanner } from "../ui/Banner";
import { messages } from "../ui/messages";
import { useToast } from "../ui/Toast";
import { toldList } from "./told";
import { keys } from "./works";

const making = new Set<Project["state"]>(["uploading", "queued", "generating"]);
const BANNERS = ["upload", "queue", "download"];
const { tell } = toldList("videocut-told");

// download starts the exported film's download. A browser may block it
// without saying so, so the banner keeps a link to start it by hand.
function download(work: Project) {
  const link = document.createElement("a");
  link.href = filmURL(work.id);
  link.download = "";
  link.click();
}

export function useWorkEvents(work: Project | undefined) {
  const toast = useToast();
  const banner = useBanner();
  const client = useQueryClient();
  const before = useRef<Project | undefined>(undefined);
  const workId = work?.id;

  // The project's banners are its own: they go when the member leaves it.
  useEffect(
    () => () => {
      for (const key of BANNERS) banner.hide(key);
    },
    [banner, workId],
  );

  useEffect(() => {
    if (!work) return;
    const last = before.current?.id === work.id ? before.current : undefined;
    before.current = work;

    if (work.state === "upload_interrupted") banner.show({ key: "upload", text: messages.uploadInterrupted });
    else banner.hide("upload");
    if (work.state === "queued") banner.show({ key: "queue", text: messages.queued });
    else banner.hide("queue");

    if (last && making.has(last.state) && work.state === "ready") toast(messages.generated);
    if (work.progress.skipped > 0 && hasFilm(work.state)) {
      if (tell(`skipped:${work.id}`)) toast(messages.skipped(work.progress.skipped));
    }

    const exportedAt = work.film?.exportedAt;
    if (work.state === "exported" && exportedAt && tell(`exported:${work.id}:${exportedAt}`)) {
      const save = { label: messages.download, run: () => download(work) };
      // Seen to finish on this page: it downloads by itself. Finished while
      // the page was not open: the member starts it (docs/specs/venus.md#边界与失败).
      if (last) {
        download(work);
        toast(messages.exported);
        banner.show({ key: "download", text: messages.downloadFallback, action: save });
      } else banner.show({ key: "download", text: messages.downloadReady, action: save });
    }

    const failed = work.exportFailure;
    if (failed && tell(`exportFailed:${work.id}:${failed.at}`)) {
      const reason = messages.failure[failed.reason];
      if (failed.retries >= 3) toast(`${messages.exportFailed}：${messages.tooManyRetries}`);
      else
        toast(`${messages.exportFailed}：${reason}`, {
          label: messages.retry,
          run: () => void exportFilm(work.id).then((next) => client.setQueryData(keys.work(next.id), next)),
        });
    }
  }, [work, toast, banner, client]);
}
