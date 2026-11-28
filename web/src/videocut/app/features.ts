// The capabilities feature components get, each over the VideoCut API. The
// signatures are the skeleton's (#695); what is marked for a feature is
// filled in by that feature's issue without changing how pages call it.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  ask,
  createProject,
  exportFilm,
  generate,
  getCatalog,
  getSongStructure,
  getTimeline,
  type LocalFile,
  listCandidates,
  listImported,
  type Project,
  resumeUpload,
  retry,
  updateSelection,
  updateSettings,
} from "../api/client";
import { useCutStore } from "./cutStore";
import { keys, useSetWork } from "./works";

/** The songs, aspects and styles a film can take (M4). */
export const useCatalog = () =>
  useQuery({ queryKey: keys.catalog, queryFn: ({ signal }) => getCatalog(signal), staleTime: Infinity });

/** The film as it is, once made. */
export const useTimeline = (workId: string, enabled = true) =>
  useQuery({ queryKey: keys.timeline(workId), queryFn: ({ signal }) => getTimeline(workId, signal), enabled });

/** The parts and slots the song makes, before the timeline is (the tracks of 生成中). */
export const useSongStructure = (workId: string, enabled = true) =>
  useQuery({
    queryKey: keys.song(workId),
    queryFn: ({ signal }) => getSongStructure(workId, signal),
    retry: false,
    enabled,
  });

/** A slot's candidates, at most six (M7-1). */
export const useCandidates = (workId: string, slot: number | null) =>
  useQuery({
    queryKey: keys.candidates(workId, slot ?? 0),
    queryFn: ({ signal }) => listCandidates(workId, slot ?? 0, signal),
    enabled: slot !== null,
  });

/** 添加素材 · 已导入素材 (M7-4): the project's uploaded and analysed videos. */
export const useImported = (workId: string) =>
  useQuery({ queryKey: keys.imported(workId), queryFn: ({ signal }) => listImported(workId, signal) });

// useWorkMutation is an operation that answers with the project: the
// answer goes into the cache, so the shell moves on without the next read.
function useWorkMutation<T = void>(call: (input: T) => Promise<Project>) {
  const setWork = useSetWork();
  return useMutation({ mutationFn: call, onSuccess: setWork });
}

/** 勾选 and 星标 (M3-3), saved as they change. */
export function useSelection(workId: string) {
  const mutation = useWorkMutation((files: { fileId: string; selected?: boolean; starred?: boolean }[]) =>
    updateSelection(workId, files),
  );
  return {
    select: (fileId: string, selected: boolean) => mutation.mutateAsync([{ fileId, selected }]),
    star: (fileId: string, starred: boolean) => mutation.mutateAsync([{ fileId, starred }]),
    saving: mutation.isPending,
  };
}

/** The song, aspect and style (M4), saved as they change. */
export function useSettings(workId: string) {
  const mutation = useWorkMutation((change: { aspect?: string; style?: string; builtinSong?: string }) =>
    updateSettings(workId, change),
  );
  return { set: mutation.mutateAsync, saving: mutation.isPending };
}

/** 开始生成 (M4-5) and retrying a failed project (M5-3). */
export function useGenerate(workId: string) {
  const start = useWorkMutation(() => generate(workId));
  const again = useWorkMutation(() => retry(workId));
  return { start: start.mutateAsync, retry: again.mutateAsync, starting: start.isPending || again.isPending };
}

/**
 * The upload of the selected files. Handing files to the desktop's upload
 * queue and to the project (POST .../assets with fileId) is the 导入与上传
 * feature's; the skeleton has going on after an interruption.
 */
export function useUpload(workId: string) {
  const resume = useWorkMutation(() => resumeUpload(workId));
  return { resume: resume.mutateAsync };
}

/**
 * The files dropped on the start page, before a project is made of them
 * (M2). Reading them in the browser, their first frames and the 4 readers at
 * a time (resources.ts) are the 导入与上传 feature's; the skeleton keeps the
 * list and makes the project.
 */
export function useLocalImport() {
  const [files, setFiles] = useState<File[]>([]);
  const setWork = useSetWork();
  const make = useMutation({
    mutationFn: () => createProject(files.map(listed)),
    onSuccess: (work) => {
      setWork(work);
      setFiles([]);
    },
  });
  return { files, add: (more: File[]) => setFiles((all) => [...all, ...more]), create: make.mutateAsync };
}

const listed = (file: File): LocalFile => ({
  name: file.name,
  size: file.size,
  modifiedAt: new Date(file.lastModified).toISOString(),
  read: false,
});

/** A sentence to the assistant (M8), about a shot when slot is given. */
export function useAssistant(workId: string) {
  const mutation = useMutation({
    mutationFn: ({ text, slot }: { text: string; slot?: number }) => ask(workId, text, slot),
  });
  return { send: mutation.mutateAsync, sending: mutation.isPending };
}

/**
 * 导出 (M9-1). The edits are locked from the click, and stay locked once
 * the project is exporting: the shell turns the page read-only then, so
 * there is no moment between.
 */
export function useExport(workId: string) {
  const store = useCutStore();
  const setWork = useSetWork();
  const client = useQueryClient();
  return {
    start: async () => {
      store.set({ busy: true });
      try {
        setWork(await exportFilm(workId));
        await client.invalidateQueries({ queryKey: keys.works });
      } finally {
        store.set({ busy: false });
      }
    },
  };
}

/**
 * The desktop's 问题反馈, opened from Venus (M1-3, M9-3). The desktop's
 * FeedbackChannel opens only from its own button for now; opening it from
 * elsewhere is shared code for main (T11), so this is not available yet.
 */
export function useFeedback() {
  return { available: false, open: (_source: string) => undefined };
}
