import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useEffectEvent, useRef, useState } from "react";

import { errorCodeText, errorText } from "../api/errorMessages";
import { listSpaces } from "../files/api";
import { getMusicStatus, listSources, listTracks, type MusicStatus, scanMusic } from "../music/api";
import { getTransfer } from "../transfers/api";
import { failureText } from "../transfers/model";
import { transferStore, useTransfers } from "../transfers/store";
import { chooseSong } from "./api";
import { songKey } from "./queries";

// Limits of a song, as the montage center holds them (internal/montage/songs.go).
const maxSongBytes = 100 << 20;
const minSongSeconds = 30;
const maxSongSeconds = 8 * 60;
// The audio files the music library indexes (internal/music/metadata.go, formats).
const songTypes = [
  ".mp3",
  ".flac",
  ".m4a",
  ".aac",
  ".ogg",
  ".oga",
  ".opus",
  ".wav",
  ".ape",
  ".wma",
  ".wv",
  ".aif",
  ".aiff",
  ".dsf",
  ".dff",
];
export const acceptedSongTypes = songTypes.join(",");
// Where an uploaded song goes: this folder of the member's private space,
// which the NAS creates when it is not there yet.
const songFolder = "音乐";
// How often the music library's scan is asked about, as the music window does.
const statusPoll = 1500;

const isSong = (name: string) => {
  const dot = name.lastIndexOf(".");
  return dot >= 0 && songTypes.includes(name.slice(dot).toLowerCase());
};

// audioSeconds is how long the browser says the file plays, or undefined
// when it cannot read it: the montage center decides about those.
function audioSeconds(file: File): Promise<number | undefined> {
  if (typeof URL.createObjectURL !== "function") return Promise.resolve(undefined);
  return new Promise((resolve) => {
    const probe = document.createElement("audio");
    const url = URL.createObjectURL(file);
    const done = (seconds?: number) => {
      window.clearTimeout(timer);
      probe.removeAttribute("src");
      URL.revokeObjectURL(url);
      resolve(seconds !== undefined && Number.isFinite(seconds) && seconds > 0 ? seconds : undefined);
    };
    const timer = window.setTimeout(() => done(), 5000);
    probe.preload = "metadata";
    probe.onloadedmetadata = () => done(probe.duration);
    probe.onerror = () => done();
    probe.src = url;
  });
}

// What the montage center would refuse, told before the file is sent.
async function refusal(file: File): Promise<string | undefined> {
  if (!isSong(file.name)) return errorCodeText("not_audio", "montage");
  if (file.size > maxSongBytes) return errorCodeText("song_too_large", "montage");
  const seconds = await audioSeconds(file);
  if (seconds !== undefined && (seconds < minSongSeconds || seconds > maxSongSeconds))
    return errorCodeText("song_length", "montage");
  return undefined;
}

const scanProblems: Record<NonNullable<MusicStatus["scan"]["error"]>, string> = {
  volume_unavailable: "数据卷不可用，音乐库没有扫描。数据卷恢复后再选",
  failed: "音乐库扫描失败。在音乐里重新扫描后再选",
};

class Refused extends Error {}

export type SongUploadStep = "checking" | "upload" | "scan" | "choose";
export interface SongUpload {
  name: string;
  step: SongUploadStep;
  // The upload in the transfer center's queue.
  key?: string;
  problem?: string;
}

// useSongUpload sends a song the member picks into the 音乐 folder of their
// private space through the transfer center's queue, scans the music library
// until the song is in it, and chooses it for the project. Leaving the page
// leaves the upload to the transfer center; the song is then chosen by hand.
export function useSongUpload(projectId: string, userId: string) {
  const client = useQueryClient();
  const [job, setJob] = useState<SongUpload>();
  const snapshot = useTransfers();
  const upload = job?.key ? snapshot.uploads.find((item) => item.key === job.key) : undefined;
  const stopped = useRef(new AbortController());
  useEffect(() => {
    const controller = new AbortController();
    stopped.current = controller;
    return () => controller.abort();
  }, []);
  const settling = useRef<string>(undefined);

  const pick = async (file: File) => {
    const signal = stopped.current.signal;
    setJob({ name: file.name, step: "checking" });
    const fail = (problem: string) => !signal.aborted && setJob({ name: file.name, step: "checking", problem });
    const refused = await refusal(file);
    if (refused) return fail(refused);
    let spaceId: string | undefined;
    try {
      // The member's own space, never one an administrator only views.
      const spaces = await listSpaces();
      spaceId = spaces.find((space) => space.kind === "private" && !space.viewing && space.ownerUserId === userId)?.id;
    } catch (caught) {
      return fail(errorText(caught));
    }
    if (!spaceId) return fail("找不到你的个人空间，歌没有上传");
    if (signal.aborted) return;
    const batch = transferStore.enqueue(
      [{ file, directories: [songFolder] }],
      { spaceId, spaceKind: "private", parentId: "", folders: [], label: `剪辑中心 · ${songFolder}` },
      // A song of the same name stays; this one is saved beside it.
      { choice: "rename" },
    );
    const key = transferStore.getSnapshot().uploads.find((item) => item.batch === batch)?.key;
    setJob({ name: file.name, step: "upload", key });
  };

  // Once uploaded: the library scans its folders again (a scan asked for
  // while one runs runs once more after it), and the song, a track named by
  // its file, is chosen when it is there.
  const settle = useEffectEvent(async (name: string, key: string, transferId: string) => {
    const signal = stopped.current.signal;
    const step = (next: SongUploadStep, problem?: string) => {
      if (!signal.aborted) setJob({ name, key, step: next, problem });
    };
    step("scan");
    try {
      const { entryId } = await getTransfer(transferId);
      let status = await scanMusic();
      while (status.scan.state === "scanning") {
        await new Promise((resolve) => window.setTimeout(resolve, statusPoll));
        if (signal.aborted) return;
        status = await getMusicStatus();
      }
      if (status.scan.error) throw new Refused(scanProblems[status.scan.error]);
      const tracks = await client.fetchQuery({ queryKey: ["music", "tracks"], queryFn: listTracks, staleTime: 0 });
      if (!entryId || !tracks.items.some((track) => track.id === entryId)) {
        const sources = await listSources();
        throw new Refused(
          sources.custom
            ? `歌已存进“${songFolder}”文件夹，但它不在音乐库的来源里。在音乐里添加这个文件夹后再选`
            : `歌已存进“${songFolder}”文件夹，但音乐库读不出它的格式。换一种格式再传`,
        );
      }
      if (signal.aborted) return;
      step("choose");
      const chosen = await chooseSong(projectId, entryId);
      client.setQueryData(songKey(projectId), chosen);
      if (!signal.aborted) setJob(undefined);
    } catch (caught) {
      step("scan", caught instanceof Refused ? caught.message : errorText(caught, "montage"));
    }
  });
  const finished = job?.step === "upload" && upload?.phase === "completed" ? upload.transferId : undefined;
  useEffect(() => {
    if (!job?.key || !finished || settling.current === job.key) return;
    settling.current = job.key;
    void settle(job.name, job.key, finished);
  }, [job, finished]);

  // How the upload stands while it is in the queue.
  let problem = job?.problem;
  let retry: (() => void) | undefined;
  if (job?.step === "upload" && job.key) {
    const key = job.key;
    if (!upload) problem = "上传已取消";
    else if (upload.phase === "failed" || upload.phase === "canceled") {
      problem =
        upload.phase === "canceled"
          ? "上传已取消"
          : upload.error?.code === "forbidden"
            ? "没有权限上传到“音乐”文件夹"
            : failureText(upload.error?.code ?? "");
      retry = () => transferStore.retry(key);
    }
  }
  const progress = upload?.size ? Math.min(1, upload.transferred / upload.size) : 0;
  return {
    job: job && { ...job, problem },
    progress,
    // Whether the file waits in the queue rather than being sent.
    queued: Boolean(upload && upload.phase !== "running"),
    busy: Boolean(job && !problem),
    pick: (file: File) => void pick(file),
    retry,
    dismiss: () => setJob(undefined),
  };
}
