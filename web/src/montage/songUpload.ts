import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useEffectEvent, useState } from "react";

import { type ErrorDomain, errorCodeText, errorText } from "../api/errorMessages";
import { listSpaces } from "../files/api";
import { getMusicStatus, listSources, listTracks, scanMusic, statusPoll } from "../music/api";
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

// What the montage center would refuse, told before the file is sent: the
// code of its answer.
async function refusal(file: File): Promise<string | undefined> {
  if (!isSong(file.name)) return "not_audio";
  if (file.size > maxSongBytes) return "song_too_large";
  const seconds = await audioSeconds(file);
  if (seconds !== undefined && (seconds < minSongSeconds || seconds > maxSongSeconds)) return "song_length";
  return undefined;
}

// Refused is what the page tells of an upload itself, by a code whose words
// are with the montage center's other messages (api/errorMessages.ts).
class Refused extends Error {}
const told = (code: string) => errorCodeText(code, "montage") ?? errorText(undefined);
const refused = (code: string) => new Refused(told(code));
const problemOf = (error: unknown, domain?: ErrorDomain) =>
  error instanceof Refused ? error.message : errorText(error, domain);

// The scan an uploaded file waits for, by the transfer the upload became.
const scanKey = (transferId?: string) => ["montage", "song-upload", transferId] as const;

export type SongUploadStep = "checking" | "upload" | "scan" | "choose";
export interface SongUpload {
  name: string;
  step: SongUploadStep;
  problem?: string;
}

// useSongUpload sends a song the member picks into the 音乐 folder of their
// private space through the transfer center's queue, scans the music library
// until the song is in it, and chooses it for the project. Leaving the page
// leaves the upload to the transfer center; the song is then chosen by hand:
// each step starts the next from the page, which is no longer there.
export function useSongUpload(projectId: string, userId: string) {
  const client = useQueryClient();
  // The song picked, and its upload in the transfer center's queue once it
  // is there.
  const [sent, setSent] = useState<{ name: string; key?: string }>();
  const snapshot = useTransfers();
  const upload = sent?.key ? snapshot.uploads.find((item) => item.key === sent.key) : undefined;
  const landed = upload?.phase === "completed" ? upload.transferId : undefined;

  // Before the file is sent: what would be refused, and where it goes.
  const check = useMutation({
    mutationFn: async (file: File) => {
      const code = await refusal(file);
      if (code) throw refused(code);
      // The member's own space, never one an administrator only views.
      const spaces = await listSpaces();
      const own = spaces.find((space) => space.kind === "private" && !space.viewing && space.ownerUserId === userId);
      if (!own) throw refused("song_space_missing");
      return own.id;
    },
  });
  // Once uploaded: the file's entry, and a scan of the library's folders (a
  // scan asked for while one runs runs once more after it).
  const scan = useMutation({
    mutationFn: async (transferId: string) => {
      const { entryId } = await getTransfer(transferId);
      client.setQueryData(scanKey(transferId), await scanMusic());
      return entryId;
    },
  });
  // The scan is asked about, as often as the music window asks, until it ends.
  const scanned = useQuery({
    queryKey: scanKey(scan.variables),
    queryFn: getMusicStatus,
    enabled: scan.isSuccess,
    staleTime: statusPoll,
    refetchInterval: (query) => (query.state.data?.scan.state === "scanning" ? statusPoll : false),
  });
  const ended = scan.isSuccess && scanned.data?.scan.state === "idle" ? scanned.data.scan : undefined;
  // The song is a track named by its file, when the library has read it.
  const find = useMutation({
    mutationFn: async (entryId: string | undefined) => {
      const tracks = await client.fetchQuery({ queryKey: ["music", "tracks"], queryFn: listTracks, staleTime: 0 });
      if (!entryId || !tracks.items.some((track) => track.id === entryId))
        throw refused((await listSources()).custom ? "song_not_in_sources" : "song_not_indexed");
      return entryId;
    },
  });
  const choose = useMutation({ mutationFn: (trackId: string) => chooseSong(projectId, trackId) });

  const clear = () => {
    setSent(undefined);
    check.reset();
    scan.reset();
    find.reset();
    choose.reset();
  };
  const pick = (file: File) => {
    clear();
    setSent({ name: file.name });
    check.mutate(file, {
      onSuccess: (spaceId) => {
        const batch = transferStore.enqueue(
          [{ file, directories: [songFolder] }],
          { spaceId, spaceKind: "private", parentId: "", folders: [], label: `剪辑中心 · ${songFolder}` },
          // A song of the same name stays; this one is saved beside it.
          { choice: "rename" },
        );
        setSent({
          name: file.name,
          key: transferStore.getSnapshot().uploads.find((item) => item.batch === batch)?.key,
        });
      },
    });
  };

  // The upload has landed: scan. The scan has ended: find the song and
  // choose it.
  const scanFor = useEffectEvent((transferId: string) => scan.mutate(transferId));
  const unscanned = scan.isIdle;
  useEffect(() => {
    if (landed && unscanned) scanFor(landed);
  }, [landed, unscanned]);
  const settle = useEffectEvent(() =>
    find.mutate(scan.data, {
      onSuccess: (trackId) =>
        choose.mutate(trackId, {
          onSuccess: (chosen) => {
            client.setQueryData(songKey(projectId), chosen);
            clear();
          },
        }),
    }),
  );
  const found = Boolean(ended && !ended.error && find.isIdle);
  useEffect(() => {
    if (found) settle();
  }, [found]);

  let step: SongUploadStep = "checking";
  if (!choose.isIdle) step = "choose";
  else if (!scan.isIdle) step = "scan";
  else if (sent?.key) step = "upload";

  // What stopped it, if anything; a failed upload can be sent again.
  let problem: string | undefined;
  let retry: (() => void) | undefined;
  const failure = check.error ?? scan.error ?? scanned.error ?? find.error;
  if (choose.error) problem = problemOf(choose.error, "montage");
  else if (failure) problem = problemOf(failure);
  else if (ended?.error) problem = told(ended.error === "failed" ? "song_scan_failed" : ended.error);
  else if (step === "upload" && sent?.key) {
    const key = sent.key;
    if (!upload || upload.phase === "canceled") problem = told("song_upload_canceled");
    else if (upload.phase === "failed") problem = failureText(upload.error?.code ?? "");
    if (upload && problem) retry = () => transferStore.retry(key);
  }
  const progress = upload?.size ? Math.min(1, upload.transferred / upload.size) : 0;
  const job: SongUpload | undefined = sent && { name: sent.name, step, problem };
  return {
    job,
    progress,
    // Whether the file waits in the queue rather than being sent.
    queued: Boolean(upload && upload.phase !== "running"),
    busy: Boolean(sent && !problem),
    pick,
    retry,
    dismiss: clear,
  };
}
