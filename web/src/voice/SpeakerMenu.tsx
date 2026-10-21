import { Check, Fingerprint, GitMerge, UserRoundCheck, UserRoundX, X } from "lucide-react";
import { type FormEvent, ReactNode, useState } from "react";

import { messageOf, useVoice, VoiceMark } from "./context";
import { duration } from "./format";
import { Popover } from "./Popover";
import { assignSpeaker, listPeople, mergeSpeakers, type Person, type RecordingDetail, rejectPerson, type Speaker } from "./voiceApi";

interface Props {
  detail: RecordingDetail;
  speaker: Speaker;
  onChange: (detail: RecordingDetail) => void;
  children: ReactNode;
  className?: string;
}

// SpeakerMenu names a speaker, answers a suggestion, says 不是他 or merges
// two voices. Naming teaches the voiceprint, so the same voice is named in
// the member's other recordings too.
export function SpeakerMenu({ detail, speaker, onChange, children, className = "vo-speaker-chip" }: Props) {
  const voice = useVoice();
  const [people, setPeople] = useState<Person[]>([]);
  const [name, setName] = useState("");
  const [working, setWorking] = useState(false);
  const run = async (action: () => Promise<RecordingDetail>, done: string, close: () => void) => {
    setWorking(true);
    try {
      onChange(await action());
      voice.changed();
      voice.toast(done);
      close();
    } catch (caught) {
      voice.toast(messageOf(caught), "error");
    } finally {
      setWorking(false);
    }
  };
  const others = detail.speakerList.filter((other) => other.key !== speaker.key);
  const matches = people.filter((person) => person.name !== speaker.label && (!name.trim() || person.name.includes(name.trim()))).slice(0, 6);
  return (
    <Popover
      label={`${speaker.label}，点击命名`}
      className={className}
      align="start"
      trigger={children}
      onOpenChange={(open) => {
        if (!open) return;
        setName(speaker.personId ? speaker.label : "");
        void listPeople().then(setPeople).catch(() => setPeople([]));
      }}
    >
      {(close) => {
        const submit = (event: FormEvent) => {
          event.preventDefault();
          const value = name.trim();
          if (!value || value === speaker.label) { close(); return; }
          const existing = people.find((person) => person.name === value);
          void run(() => assignSpeaker(detail.id, speaker.key, existing ? { personId: existing.id } : { name: value }), `已命名为“${value}”`, close);
        };
        return (
          <div className="vo-speaker-panel">
            <header>
              <VoiceMark speaker={speaker} size={36} />
              <span>
                <strong>{speaker.label}</strong>
                <small>说了 {duration(speaker.speechMs)}{speaker.assigned === "auto" ? ` · 按声纹认出（相似度 ${Math.round((speaker.score ?? 0) * 100)}）` : speaker.assigned === "user" ? " · 你命名的" : ""}</small>
              </span>
            </header>
            {speaker.suggestion && (
              <div className="vo-speaker-suggest">
                <Fingerprint />
                <span>声音很像 <strong>{speaker.suggestion.name}</strong></span>
                <button type="button" className="vo-button small primary" disabled={working}
                  onClick={() => void run(() => assignSpeaker(detail.id, speaker.key, { personId: speaker.suggestion!.personId }), `已确认是${speaker.suggestion!.name}`, close)}>
                  <Check />是 TA
                </button>
                <button type="button" className="vo-button small" disabled={working}
                  onClick={() => void run(() => rejectPerson(detail.id, speaker.key, speaker.suggestion!.personId), "以后不会再这样建议", close)}>
                  不是
                </button>
              </div>
            )}
            <form className="vo-speaker-name-form" onSubmit={submit}>
              <label htmlFor={`name-${speaker.key}`}>{speaker.personId ? "改为" : "这是谁？"}</label>
              <div className="vo-inline">
                <input id={`name-${speaker.key}`} value={name} maxLength={40} autoFocus placeholder="输入名字" autoComplete="off" onChange={(event) => setName(event.target.value)} />
                <button type="submit" className="vo-button primary" disabled={working || !name.trim()}>确定</button>
              </div>
              {matches.length > 0 && (
                <div className="vo-person-picks" aria-label="已有的人">
                  {matches.map((person) => (
                    <button key={person.id} type="button" disabled={working}
                      onClick={() => void run(() => assignSpeaker(detail.id, speaker.key, { personId: person.id }), `已命名为“${person.name}”`, close)}>
                      <UserRoundCheck />{person.name}
                    </button>
                  ))}
                </div>
              )}
              {speaker.voiceprint
                ? <small className="vo-speaker-hint">命名后会记住这个声音，之后的录音里自动认出 TA。声纹只保存在这台 NAS。</small>
                : <small className="vo-speaker-hint">这位发言人说得太少，没有声纹；名字只用在这段录音里。</small>}
            </form>
            <div className="vo-speaker-actions">
              {speaker.personId && (
                <button type="button" disabled={working} onClick={() => void run(() => rejectPerson(detail.id, speaker.key, speaker.personId!), `已清除“${speaker.label}”`, close)}>
                  {speaker.assigned === "auto" ? <UserRoundX /> : <X />}{speaker.assigned === "auto" ? `不是${speaker.label}` : "清除名字"}
                </button>
              )}
              {others.length > 0 && (
                <div className="vo-merge">
                  <span><GitMerge />和另一位是同一个人</span>
                  <div>
                    {others.map((other) => (
                      <button key={other.key} type="button" disabled={working}
                        onClick={() => void run(() => mergeSpeakers(detail.id, speaker.key, other.key), `已合并到${other.label}`, close)}>
                        <VoiceMark speaker={other} size={18} />{other.label}
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </div>
        );
      }}
    </Popover>
  );
}
