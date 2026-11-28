// 这一镜 (M7): the selected shot, its candidates (M7-1), and the slots of
// the shot's other operations. M7-1 is the sample the other operations
// follow: read through a capability (useCandidates), change through
// useEditor, and leave the lock, the replay and the toast to them.
import { useCut } from "../../app/cutStore";
import { useEditor } from "../../app/editor";
import { useCandidates, useTimeline } from "../../app/features";
import { frameURL } from "../../app/media";
import { clock, duration } from "../../lib/time";
import { ActionButton } from "../../ui/ActionButton";
import { Empty } from "../../ui/Empty";
import { messages } from "../../ui/messages";
import { Slot } from "../../ui/Slot";
import styles from "./cut.module.css";

export function ThisShot({ workId, slot }: { workId: string; slot: number }) {
  const shot = useTimeline(workId, false).data?.shots.find((s) => s.slot === slot);
  if (!shot) return null;
  return (
    <section className={styles.thisShot} aria-label={messages.thisShot}>
      <h2>{messages.shot(slot)}</h2>
      <img className={styles.still} src={frameURL(workId, shot.assetId, shot.from)} alt="" />
      <p className={styles.facts}>
        <span>{`${clock(shot.start)}–${clock(shot.end)}`}</span>
        <span>{duration(shot.end - shot.start)}</span>
        <span>{messages.beats(shot.musicBeats)}</span>
      </p>
      <Candidates workId={workId} slot={slot} />
      <Slot id="M7-2" />
      <Slot id="M7-3" />
      <Slot id="M7-4" />
    </section>
  );
}

// Candidates (M7-1): at most six beats the slot could take; one click
// replaces the shot. ActionButton greys them while an edit is made or the
// film exports.
function Candidates({ workId, slot }: { workId: string; slot: number }) {
  const candidates = useCandidates(workId, slot);
  const editor = useEditor(workId);
  const why = useCut((state) =>
    state.sample ? messages.sampleReadOnly : state.mode === "exporting" ? messages.readOnly : undefined,
  );
  const items = candidates.data?.items;
  return (
    <section aria-label={messages.candidates} className={styles.candidates}>
      <h3>{messages.candidates}</h3>
      {why && <p className={styles.facts}>{why}</p>}
      {candidates.isPending && <p className={styles.facts}>{messages.loading}</p>}
      {candidates.isError && (
        <Empty
          text={messages.candidatesFailed}
          action={
            <button type="button" onClick={() => void candidates.refetch()}>
              {messages.retry}
            </button>
          }
        />
      )}
      {items?.length === 0 && <Empty text={messages.noCandidates} />}
      <div className={styles.candidateGrid}>
        {items?.map((candidate, i) => (
          <ActionButton
            key={candidate.beat}
            className={styles.candidate}
            aria-label={messages.candidate(i + 1)}
            onClick={() => void editor.replace(slot, candidate.beat)}
          >
            <img src={frameURL(workId, candidate.assetId, candidate.from)} alt="" />
            <span>{duration(candidate.duration)}</span>
          </ActionButton>
        ))}
      </div>
    </section>
  );
}
