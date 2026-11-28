import { PanelRight } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import type { Project } from "../../api/client";
import { CutProvider, createCutStore, readOnly, useCut, useCutStore } from "../../app/cutStore";
import { useEditor } from "../../app/editor";
import { useSongStructure, useTimeline } from "../../app/features";
import { useNav } from "../../app/nav";
import { useFollowSelection, usePlayer } from "../../app/player";
import { type CutMode, cutModeOf, statusLabel } from "../../lib/pages";
import { ActionLock } from "../../ui/lock";
import { messages } from "../../ui/messages";
import { Slot } from "../../ui/Slot";
import styles from "../layout.module.css";

// The film page (M5–M9): top bar, player, inspector and tracks. The layout,
// the shared state, the 生成中 tracks and the folding inspector are the
// skeleton's; each feature replaces its slot.
export function CutPage({ work }: { work: Project }) {
  const [store] = useState(() => createCutStore({ mode: cutModeOf(work.state), sample: work.sample }));
  // The page's mode is the project's state, and only the shell writes it.
  useEffect(() => store.set({ mode: cutModeOf(work.state) }), [store, work.state]);
  return (
    <CutProvider store={store}>
      <Film work={work} />
    </CutProvider>
  );
}

function Film({ work }: { work: Project }) {
  const nav = useNav();
  const mode = useCut((state) => state.mode);
  const locked = useCut(readOnly);
  const editor = useEditor(work.id);
  useTimeline(work.id, mode !== "generating" && mode !== "failed");
  useFollowSelection(work.id);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const label = statusLabel(mode);

  // Ctrl+Z (⌘Z) undoes, from anywhere on the page but a text field; the
  // shortcut goes with the page (页面级).
  const page = useRef<HTMLDivElement>(null);
  const undo = useRef(editor.undo);
  useEffect(() => {
    undo.current = editor.undo;
  });
  useEffect(() => {
    const element = page.current;
    if (!element) return;
    const onKeyDown = (event: KeyboardEvent) => {
      const typing = event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement;
      if (!typing && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "z") {
        event.preventDefault();
        void undo.current();
      }
    };
    element.addEventListener("keydown", onKeyDown);
    return () => element.removeEventListener("keydown", onKeyDown);
  }, []);

  return (
    <ActionLock.Provider value={locked}>
      <div className={styles.cut} ref={page}>
        <header className={styles.topBar}>
          <button type="button" className={styles.back} onClick={nav.toStart}>
            {messages.back}
          </button>
          <h1>{work.name}</h1>
          {label && <span className={styles.status}>{label}</span>}
          <span className={styles.spacer} />
          <Slot id="M6-5" />
          <Slot id="M9-1" />
          <Slot id="M9-3" />
        </header>
        <div className={styles.cutMain}>
          <div className={styles.center}>
            <Center mode={mode} workId={work.id} />
          </div>
          <div className={styles.rail}>
            <button
              type="button"
              className={styles.back}
              title={messages.inspector}
              aria-expanded={inspectorOpen}
              onClick={() => setInspectorOpen((open) => !open)}
            >
              <PanelRight size={18} />
            </button>
          </div>
          <aside className={styles.inspector} data-open={inspectorOpen} aria-label={messages.inspector}>
            <Inspector mode={mode} />
          </aside>
          {mode === "exporting" && (
            <div className={styles.overlay}>
              <Slot id="M9-2" />
            </div>
          )}
        </div>
        <Tracks mode={mode} workId={work.id} />
      </div>
    </ActionLock.Provider>
  );
}

const clock = (seconds: number) => `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2, "0")}`;

function Center({ mode, workId }: { mode: CutMode; workId: string }) {
  const { playhead } = usePlayer(workId);
  if (mode === "generating") return <Slot id="M5-1" />;
  if (mode === "failed") return <Slot id="M5-3" />;
  return (
    <Slot id="M6-1">
      <output aria-label={messages.playhead}>{clock(playhead)}</output>
    </Slot>
  );
}

// The inspector shows 这一镜 while a shot is selected, the assistant
// otherwise, and the settings read only while the film is being made.
function Inspector({ mode }: { mode: CutMode }) {
  const selected = useCut((state) => state.selectedSlot);
  if (mode === "generating" || mode === "failed") return <Slot id="M5-2" />;
  if (selected !== null) return <Slot id="M7">{messages.shot(selected)}</Slot>;
  return <Slot id="M8" />;
}

// The tracks: parts, music and shots. While the film is being made the
// parts are drawn from the song, the music shows, and the shots say they
// come once it is made (M5-1).
function Tracks({ mode, workId }: { mode: CutMode; workId: string }) {
  const making = mode === "generating" || mode === "failed";
  return (
    <div className={styles.tracks}>
      <Slot id="M6-3">
        <Parts workId={workId} making={making} />
      </Slot>
      <Slot id="M6-4" />
      <Slot id="M6-2">{making ? <p>{messages.shotsAfterGenerating}</p> : <Shots workId={workId} />}</Slot>
    </div>
  );
}

function Parts({ workId, making }: { workId: string; making: boolean }) {
  const store = useCutStore();
  const song = useSongStructure(workId);
  const timeline = useTimeline(workId, false);
  const parts = (making ? song.data?.parts : timeline.data?.parts) ?? [];
  return (
    <div className={styles.parts}>
      {parts.map((part) => (
        <button
          key={part.index}
          type="button"
          className={styles.part}
          disabled={making}
          onClick={() => store.set({ activePart: part.index, playhead: part.start })}
        >
          {part.label}
        </button>
      ))}
    </div>
  );
}

// Shots stands in for M6-2's cards until the feature: a click selects the
// shot and nothing else.
function Shots({ workId }: { workId: string }) {
  const store = useCutStore();
  const selected = useCut((state) => state.selectedSlot);
  const shots = useTimeline(workId, false).data?.shots ?? [];
  return (
    <div className={styles.shots}>
      {shots.map((shot) => (
        <button
          key={shot.slot}
          type="button"
          className={styles.shot}
          aria-pressed={selected === shot.slot}
          onClick={() => store.set({ selectedSlot: shot.slot })}
        >
          {messages.shot(shot.slot)}
        </button>
      ))}
    </div>
  );
}
