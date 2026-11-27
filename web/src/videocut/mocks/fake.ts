// VenusFake is VideoCut as the contract describes it (api/paths/videocut.yaml),
// kept in memory, for the MSW stand-ins (handlers.ts) that answer the Web
// until VideoCut serves Venus (#694). It follows the project state machine of
// docs/specs/venus.md: a read of a project in a state that moves by itself
// (uploading, queued, generating, exporting) moves it one step, so a page that
// polls goes through them. Nothing here is a product rule to copy: films are
// made of made-up beats.
import type {
  VideoCutAssistantReply,
  VideoCutCandidate,
  VideoCutCandidates,
  VideoCutCatalog,
  VideoCutEdit,
  VideoCutFailure,
  VideoCutLocalFile,
  VideoCutPart,
  VideoCutProject,
  VideoCutProjectAsset,
  VideoCutProjectFile,
  VideoCutProjectList,
  VideoCutProjectSong,
  VideoCutProjectSummary,
  VideoCutShot,
  VideoCutSongStructure,
  VideoCutTimeline,
} from "../../api.generated";

// FakeError is an answer other than success: its status and error code.
export class FakeError extends Error {
  constructor(
    readonly status: 400 | 403 | 404 | 409 | 422 | 503,
    readonly code: string,
  ) {
    super(code);
  }
}

export const SAMPLE_ID = "sample";
const MAX_FILES = 200;
const MAX_STARS = 5;
const MAX_RETRIES = 3;
const CANDIDATES = 6;

export const catalog: VideoCutCatalog = {
  songs: [
    { id: "builtin:summer", title: "夏日", duration: 96 },
    { id: "builtin:road", title: "在路上", duration: 120 },
    { id: "builtin:home", title: "回家", duration: 88 },
  ],
  aspects: [
    { id: "16:9", label: "横屏 16:9" },
    { id: "9:16", label: "竖屏 9:16" },
  ],
  styles: [
    { id: "natural", label: "自然" },
    { id: "lively", label: "轻快" },
  ],
  defaults: { song: "builtin:summer", aspect: "16:9", style: "natural" },
};

// The film every song makes here: four parts of 2-second slots, 30 seconds.
const SECTIONS: [label: string, slots: number][] = [
  ["前奏", 2],
  ["主歌", 5],
  ["副歌", 6],
  ["尾奏", 2],
];
const SLOT_SECONDS = 2;

export function partsOfSong(): VideoCutPart[] {
  const parts: VideoCutPart[] = [];
  let slot = 1;
  let at = 0;
  SECTIONS.forEach(([label, count], i) => {
    const start = at;
    const slots = Array.from({ length: count }, () => {
      const span = { slot: slot++, start: at, end: at + SLOT_SECONDS };
      at += SLOT_SECONDS;
      return span;
    });
    parts.push({ index: i + 1, label, start, end: at, slots });
  });
  return parts;
}

const filmSeconds = () => partsOfSong().at(-1)?.end ?? 0;

interface Stored {
  project: VideoCutProject;
  requestKeys: Map<string, string>;
  timeline?: VideoCutTimeline;
  undo?: VideoCutTimeline;
  assets: VideoCutProjectAsset[];
}

const sameFile = (a: VideoCutLocalFile, b: VideoCutLocalFile) =>
  a.name === b.name && a.size === b.size && Date.parse(a.modifiedAt) === Date.parse(b.modifiedAt);

const isRead = (files: VideoCutProjectFile[]) => files.every((file) => file.read);

export interface FakeOptions {
  // A read of a project moves it one step; off, only step() does.
  advanceOnRead?: boolean;
  // Uploading needs nothing from the page: every selected file arrives at the next step.
  uploadsArrive?: boolean;
}

export class VenusFake {
  private readonly stored = new Map<string, Stored>();
  private clock = Date.parse("2026-12-01T09:00:00Z");
  private ids = 0;
  readonly options: Required<FakeOptions>;

  constructor(options: FakeOptions = {}) {
    this.options = { advanceOnRead: true, uploadsArrive: true, ...options };
    this.stored.set(SAMPLE_ID, this.makeSample());
  }

  private now(): string {
    this.clock += 1000;
    return new Date(this.clock).toISOString();
  }

  private nextId(prefix: string): string {
    this.ids += 1;
    return `${prefix}:${this.ids}`;
  }

  private builtinSong(id: string): VideoCutProjectSong {
    const song = catalog.songs.find((s) => s.id === id);
    if (!song) throw new FakeError(400, "validation_failed");
    return { source: "builtin", id: song.id, title: song.title, duration: song.duration, status: "ready" };
  }

  private makeSample(): Stored {
    const at = new Date(this.clock).toISOString();
    const files: VideoCutProjectFile[] = Array.from({ length: 8 }, (_, i) => ({
      id: `sample-file:${i + 1}`,
      name: `山景-${i + 1}.mp4`,
      size: 20_000_000,
      modifiedAt: at,
      read: true,
      duration: 12,
      width: 1920,
      height: 1080,
      takenAt: at,
      selected: true,
      starred: false,
      upload: "done",
      assetId: `sample-asset:${i + 1}`,
      analysis: "done",
    }));
    const project: VideoCutProject = {
      id: SAMPLE_ID,
      name: "示例",
      sample: true,
      state: "ready",
      createdAt: at,
      updatedAt: at,
      fileCount: files.length,
      progress: { files: 8, uploaded: 8, analysed: 8, skipped: 0, percent: 100, slow: false },
      film: { duration: filmSeconds() },
      coverAssetId: "sample-asset:1",
      settings: { song: this.builtinSong(catalog.defaults.song), aspect: "16:9", style: "natural", filmDuration: 30 },
      files,
    };
    const stored: Stored = { project, requestKeys: new Map(), assets: [] };
    stored.timeline = this.makeTimeline(stored);
    return stored;
  }

  // --- Reading

  list(): VideoCutProjectList {
    const own = [...this.stored.values()]
      .filter((s) => !s.project.sample)
      .map((s) => s.project)
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
    const sample = this.stored.get(SAMPLE_ID)?.project;
    return { items: [...own, ...(sample ? [sample] : [])].map(summaryOf) };
  }

  private find(id: string): Stored {
    const stored = this.stored.get(id);
    if (!stored) throw new FakeError(404, "not_found");
    return stored;
  }

  private writable(id: string): Stored {
    const stored = this.find(id);
    if (stored.project.sample) throw new FakeError(403, "sample_read_only");
    return stored;
  }

  private inState(stored: Stored, ...states: VideoCutProject["state"][]) {
    if (!states.includes(stored.project.state)) throw new FakeError(409, "project_state");
  }

  project(id: string): VideoCutProject {
    const stored = this.find(id);
    if (this.options.advanceOnRead) this.advance(stored);
    return stored.project;
  }

  // step moves a project one step on, as time would.
  step(id: string): VideoCutProject {
    const stored = this.find(id);
    this.advance(stored);
    return stored.project;
  }

  private touch(stored: Stored) {
    stored.project.updatedAt = this.now();
  }

  private advance(stored: Stored) {
    const project = stored.project;
    const progress = project.progress;
    switch (project.state) {
      case "uploading": {
        if (!this.options.uploadsArrive) return;
        for (const file of project.files) {
          if (!file.selected || file.upload === "done") continue;
          file.upload = "done";
          file.assetId ??= `asset:${file.id}`;
          file.analysis = "done";
          this.addAsset(stored, file.assetId, file.id, "done");
        }
        progress.uploaded = progress.files;
        progress.analysed = progress.files;
        const busy = [...this.stored.values()].some((s) => s !== stored && s.project.state === "generating");
        project.state = busy ? "queued" : "generating";
        progress.remainingSeconds = 60;
        break;
      }
      case "queued":
        if (![...this.stored.values()].some((s) => s.project.state === "generating")) project.state = "generating";
        break;
      case "generating":
        progress.percent = Math.min(100, progress.percent + 50);
        progress.remainingSeconds = progress.percent >= 100 ? undefined : 30;
        if (progress.percent >= 100) {
          stored.timeline = this.makeTimeline(stored);
          project.state = "ready";
          project.film = { duration: stored.timeline.duration };
          project.coverAssetId = stored.timeline.shots[0]?.assetId;
        }
        break;
      case "exporting":
        project.state = "exported";
        project.film = { duration: stored.timeline?.duration ?? 0, exportedAt: this.now(), size: 48_000_000 };
        break;
      default:
        return;
    }
    this.touch(stored);
  }

  // --- The start and prepare pages

  create(requestKey: string, files: VideoCutLocalFile[]): { project: VideoCutProject; created: boolean } {
    for (const stored of this.stored.values()) {
      const made = stored.requestKeys.get(requestKey);
      if (made === "create") return { project: stored.project, created: false };
      if (made) throw new FakeError(409, "conflict");
    }
    const at = this.now();
    const project: VideoCutProject = {
      id: this.nextId("project"),
      name: new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric" }).format(new Date(at)),
      sample: false,
      state: "importing",
      createdAt: at,
      updatedAt: at,
      fileCount: 0,
      progress: { files: 0, uploaded: 0, analysed: 0, skipped: 0, percent: 0, slow: false },
      settings: {
        song: this.builtinSong(catalog.defaults.song),
        aspect: catalog.defaults.aspect,
        style: catalog.defaults.style,
        filmDuration: filmSeconds(),
      },
      files: [],
    };
    const stored: Stored = { project, requestKeys: new Map([[requestKey, "create"]]), assets: [] };
    this.putFiles(stored, files);
    this.stored.set(project.id, stored);
    return { project, created: true };
  }

  addFiles(id: string, files: VideoCutLocalFile[]): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "importing", "pending");
    this.putFiles(stored, files);
    this.touch(stored);
    return stored.project;
  }

  private putFiles(stored: Stored, files: VideoCutLocalFile[]) {
    const project = stored.project;
    for (const local of files) {
      if (local.size > 4 * 1024 ** 3) throw new FakeError(422, "file_too_large");
      const known = project.files.find((file) => sameFile(file, local));
      if (known) {
        Object.assign(known, local);
        continue;
      }
      if (project.files.length >= MAX_FILES) throw new FakeError(422, "too_many_files");
      project.files.push({
        ...local,
        id: this.nextId("file"),
        selected: true,
        starred: false,
        upload: "none",
        analysis: "none",
      });
    }
    project.fileCount = project.files.length;
    project.state = isRead(project.files) ? "pending" : "importing";
  }

  removeFile(id: string, fileId: string): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "importing", "pending");
    const files = stored.project.files.filter((file) => file.id !== fileId);
    if (files.length === stored.project.files.length) throw new FakeError(404, "not_found");
    stored.project.files = files;
    stored.project.fileCount = files.length;
    stored.project.state = isRead(files) ? "pending" : "importing";
    this.touch(stored);
    return stored.project;
  }

  select(id: string, changes: { fileId: string; selected?: boolean; starred?: boolean }[]): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "importing", "pending");
    const next = stored.project.files.map((file) => ({ ...file }));
    for (const change of changes) {
      const file = next.find((f) => f.id === change.fileId);
      if (!file) throw new FakeError(404, "not_found");
      if (change.selected !== undefined) file.selected = change.selected;
      if (change.starred !== undefined) file.starred = change.starred;
    }
    if (next.filter((file) => file.starred).length > MAX_STARS) throw new FakeError(422, "too_many_stars");
    stored.project.files = next;
    this.touch(stored);
    return stored.project;
  }

  settings(id: string, change: { aspect?: string; style?: string; builtinSong?: string }): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "importing", "pending");
    const settings = stored.project.settings;
    if (change.aspect !== undefined) {
      if (!catalog.aspects.some((a) => a.id === change.aspect)) throw new FakeError(400, "validation_failed");
      settings.aspect = change.aspect;
    }
    if (change.style !== undefined) {
      if (!catalog.styles.some((s) => s.id === change.style)) throw new FakeError(400, "validation_failed");
      settings.style = change.style;
    }
    if (change.builtinSong !== undefined) settings.song = this.builtinSong(change.builtinSong);
    this.touch(stored);
    return stored.project;
  }

  chooseSong(id: string, song: Omit<VideoCutProjectSong, "status">): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "importing", "pending");
    stored.project.settings.song = { ...song, status: "ready" };
    this.touch(stored);
    return stored.project;
  }

  songStructure(id: string): VideoCutSongStructure {
    const song = this.find(id).project.settings.song;
    if (!song || song.status !== "ready") throw new FakeError(409, "song_not_ready");
    return { song, from: 0, to: filmSeconds(), parts: partsOfSong() };
  }

  heartbeat(id: string) {
    this.find(id);
  }

  generate(id: string, requestKey: string): VideoCutProject {
    const stored = this.writable(id);
    if (stored.requestKeys.get(requestKey) === "generate") return stored.project;
    this.inState(stored, "pending");
    const chosen = stored.project.files.filter((file) => file.selected).length;
    if (chosen === 0) throw new FakeError(422, "nothing_selected");
    if (stored.project.settings.song?.status === "unusable") throw new FakeError(422, "song_unusable");
    stored.requestKeys.set(requestKey, "generate");
    stored.project.state = "uploading";
    stored.project.progress = { files: chosen, uploaded: 0, analysed: 0, skipped: 0, percent: 0, slow: false };
    this.touch(stored);
    return stored.project;
  }

  // interrupt is what the server decides when the page stops reporting.
  interrupt(id: string): VideoCutProject {
    const stored = this.find(id);
    this.inState(stored, "uploading");
    stored.project.state = "upload_interrupted";
    this.touch(stored);
    return stored.project;
  }

  resume(id: string): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "upload_interrupted");
    stored.project.state = "uploading";
    this.touch(stored);
    return stored.project;
  }

  // fail makes a project fail at a step, as the server would.
  fail(id: string, step: VideoCutFailure["step"], reason: VideoCutFailure["reason"]): VideoCutProject {
    const stored = this.find(id);
    stored.project.state = "failed";
    stored.project.failure = { step, reason, retries: stored.project.failure?.retries ?? 0 };
    this.touch(stored);
    return stored.project;
  }

  retry(id: string): VideoCutProject {
    const stored = this.writable(id);
    this.inState(stored, "failed");
    const failure = stored.project.failure;
    if (!failure || failure.retries >= MAX_RETRIES) throw new FakeError(409, "too_many_retries");
    failure.retries += 1;
    stored.project.state = failure.step === "upload" ? "uploading" : "generating";
    this.touch(stored);
    return stored.project;
  }

  // --- Videos

  assetsOf(id: string): VideoCutProjectAsset[] {
    const stored = this.find(id);
    // An added video's analysis ends by the next look.
    const listed = stored.assets.map((asset) => ({ ...asset }));
    for (const asset of stored.assets) if (asset.analysis === "running") asset.analysis = "done";
    return listed;
  }

  private addAsset(
    stored: Stored,
    assetId: string,
    fileId: string | undefined,
    analysis: VideoCutProjectAsset["analysis"],
  ) {
    const known = stored.assets.find((asset) => asset.assetId === assetId);
    if (known) return known;
    const asset: VideoCutProjectAsset = {
      assetId,
      analysis,
      addedAt: this.now(),
      duration: 12,
      width: 1920,
      height: 1080,
    };
    if (fileId) asset.fileId = fileId;
    stored.assets.push(asset);
    return asset;
  }

  addVideo(id: string, assetId: string, fileId?: string): VideoCutProjectAsset {
    const stored = this.writable(id);
    if (fileId) {
      this.inState(stored, "uploading");
      const file = stored.project.files.find((f) => f.id === fileId);
      if (!file) throw new FakeError(404, "not_found");
      file.upload = "done";
      file.assetId = assetId;
      file.analysis = "done";
      const progress = stored.project.progress;
      progress.uploaded = stored.project.files.filter((f) => f.selected && f.upload === "done").length;
      progress.analysed = progress.uploaded;
      return this.addAsset(stored, assetId, fileId, "done");
    }
    this.inState(stored, "ready", "exported");
    return this.addAsset(stored, assetId, undefined, "running");
  }

  // --- The film page

  private makeTimeline(stored: Stored): VideoCutTimeline {
    const assets = stored.project.files.flatMap((file) => (file.selected && file.assetId ? [file.assetId] : []));
    const pool = assets.length > 0 ? assets : ["asset:none"];
    const parts = partsOfSong();
    const shots: VideoCutShot[] = parts.flatMap((part) =>
      part.slots.map((span) => {
        const assetId = pool[(span.slot - 1) % pool.length];
        return {
          slot: span.slot,
          part: part.index,
          start: span.start,
          end: span.end,
          beat: `${assetId}/${span.slot}`,
          assetId,
          from: 0,
          to: SLOT_SECONDS,
          speed: 1,
          anchor: false,
          fitted: false,
        };
      }),
    );
    return { revision: 1, duration: filmSeconds(), parts, shots, canUndo: false };
  }

  timeline(id: string): VideoCutTimeline {
    const stored = this.find(id);
    if (!stored.timeline || !["ready", "exporting", "exported"].includes(stored.project.state)) {
      throw new FakeError(409, "project_state");
    }
    return stored.timeline;
  }

  candidates(id: string, slot: number): VideoCutCandidates {
    const timeline = this.timeline(id);
    const shot = timeline.shots.find((s) => s.slot === slot);
    if (!shot) throw new FakeError(404, "not_found");
    const used = new Set(timeline.shots.map((s) => s.beat));
    const items: VideoCutCandidate[] = [];
    for (let k = 1; items.length < CANDIDATES && k <= CANDIDATES * 2; k++) {
      const beat = `${shot.assetId}/${slot}-${k}`;
      if (used.has(beat)) continue;
      items.push({ beat, assetId: shot.assetId, from: k, to: k + SLOT_SECONDS, duration: SLOT_SECONDS, fitted: false });
    }
    return { slot, items };
  }

  edit(id: string, baseRevision: number, edit: VideoCutEdit): VideoCutTimeline {
    const stored = this.writable(id);
    this.inState(stored, "ready", "exported");
    const before = this.timeline(id);
    if (baseRevision !== before.revision) throw new FakeError(409, "timeline_conflict");
    const shots = before.shots.map((shot) => ({ ...shot }));
    const at = (slot: number) => {
      const shot = shots.find((s) => s.slot === slot);
      if (!shot) throw new FakeError(422, "edit_refused");
      return shot;
    };
    let changed: number[];
    switch (edit.kind) {
      case "replace": {
        const shot = at(edit.slot);
        Object.assign(shot, {
          beat: edit.beat,
          assetId: edit.beat.split("/")[0],
          from: 0,
          to: SLOT_SECONDS,
          anchor: true,
        });
        changed = [edit.slot];
        break;
      }
      case "remove": {
        const next = this.candidates(id, edit.slot).items[0];
        if (!next) throw new FakeError(422, "edit_refused");
        Object.assign(at(edit.slot), { beat: next.beat, assetId: next.assetId, from: next.from, to: next.to });
        changed = [edit.slot];
        break;
      }
      case "set_in_point": {
        const shot = at(edit.slot);
        Object.assign(shot, { from: edit.from, to: edit.from + (shot.end - shot.start), anchor: true });
        changed = [edit.slot];
        break;
      }
      case "place": {
        if (!stored.assets.some((a) => a.assetId === edit.assetId && a.analysis === "done")) {
          throw new FakeError(422, "edit_refused");
        }
        Object.assign(at(edit.slot), {
          beat: `${edit.assetId}/placed`,
          assetId: edit.assetId,
          from: 0,
          to: SLOT_SECONDS,
          anchor: true,
        });
        changed = [edit.slot];
        break;
      }
      case "reorder_parts":
        changed = reorder(before.parts, shots, edit.from, edit.to);
        break;
    }
    stored.undo = before;
    stored.timeline = { ...before, revision: before.revision + 1, shots, canUndo: true, changedSlots: changed };
    if (stored.project.state === "exported") stored.project.state = "ready";
    this.touch(stored);
    return stored.timeline;
  }

  undo(id: string, baseRevision: number): VideoCutTimeline {
    const stored = this.writable(id);
    this.inState(stored, "ready", "exported");
    const now = this.timeline(id);
    if (baseRevision !== now.revision) throw new FakeError(409, "timeline_conflict");
    if (!stored.undo) throw new FakeError(409, "nothing_to_undo");
    const changed = now.changedSlots;
    stored.timeline = { ...stored.undo, revision: now.revision + 1, canUndo: false, changedSlots: changed };
    stored.undo = undefined;
    this.touch(stored);
    return stored.timeline;
  }

  ask(id: string, text: string, slot?: number): VideoCutAssistantReply {
    const timeline = this.timeline(id);
    const target = slot ?? timeline.shots[0]?.slot ?? 1;
    const items = this.candidates(id, target).items;
    return {
      text: `找到 ${items.length} 段和“${text}”相近的画面`,
      groups: [{ title: text, slot: target, items }],
    };
  }

  export(id: string, requestKey: string): VideoCutProject {
    const stored = this.writable(id);
    if (stored.requestKeys.get(requestKey) === "export") return stored.project;
    this.inState(stored, "ready", "exported");
    stored.requestKeys.set(requestKey, "export");
    stored.project.state = "exporting";
    stored.project.exportFailure = undefined;
    this.touch(stored);
    return stored.project;
  }

  hasFilm(id: string): boolean {
    return this.find(id).project.film?.exportedAt !== undefined;
  }

  remove(id: string) {
    this.writable(id);
    this.stored.delete(id);
  }
}

// reorder moves part from's shots to part to's place; the slots stay, and
// the shots of the parts between move along. Each part takes as many shots
// as it has slots: ones short are made up, ones over are dropped.
function reorder(parts: VideoCutPart[], shots: VideoCutShot[], from: number, to: number): number[] {
  if (from === to || !parts.some((p) => p.index === from) || !parts.some((p) => p.index === to)) {
    throw new FakeError(422, "edit_refused");
  }
  const contents = parts.map((part) => shots.filter((shot) => shot.part === part.index).map((shot) => ({ ...shot })));
  const [moved] = contents.splice(from - 1, 1);
  contents.splice(to - 1, 0, moved);
  const changed: number[] = [];
  parts.forEach((part, i) => {
    const content = contents[i];
    part.slots.forEach((span, k) => {
      const shot = shots.find((s) => s.slot === span.slot);
      const source = content[k] ?? content[content.length - 1];
      if (!shot || !source) return;
      if (shot.beat !== source.beat) changed.push(span.slot);
      Object.assign(shot, {
        beat: source.beat,
        assetId: source.assetId,
        from: source.from,
        to: source.to,
        anchor: source.anchor,
      });
    });
  });
  return changed;
}

function summaryOf(project: VideoCutProject): VideoCutProjectSummary {
  const { id, name, sample, state, createdAt, updatedAt, fileCount, progress, failure, film, coverAssetId } = project;
  return { id, name, sample, state, createdAt, updatedAt, fileCount, progress, failure, film, coverAssetId };
}
