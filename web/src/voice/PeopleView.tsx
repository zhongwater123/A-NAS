import { AudioLines, Fingerprint, GitMerge, Pencil, ShieldCheck, Trash2, UsersRound } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";

import { messageOf, Spinner, useVoice } from "./context";
import { duration, voiceColor } from "./format";
import { Popover } from "./Popover";
import { deletePerson, listPeople, mergePeople, type Person, type Recording, renamePerson } from "./voiceApi";

// PeopleView lists the people the member named, what their voiceprint was
// learned from and the recordings they speak in.
export function PeopleView({ recordings, onOpenRecording }: { recordings: Recording[]; onOpenRecording: (id: string) => void }) {
  const voice = useVoice();
  const [people, setPeople] = useState<Person[]>();
  const [renaming, setRenaming] = useState<string>();
  const [deleting, setDeleting] = useState<Person>();
  const load = useCallback(() => listPeople().then(setPeople).catch((caught) => voice.toast(messageOf(caught, "无法读取发言人"), "error")), [voice]);
  useEffect(() => { void load(); }, [load]);
  const act = async (run: () => Promise<unknown>, done: string) => {
    try {
      await run();
      voice.toast(done);
      voice.changed();
      await load();
    } catch (caught) {
      voice.toast(messageOf(caught), "error");
    }
  };
  return (
    <div className="vo-people">
      <header className="vo-page-head">
        <span className="vo-page-icon"><Fingerprint /></span>
        <div>
          <h2>发言人与声纹</h2>
          <p>在录音里给发言人起名字，A-NAS 会记住 TA 的声音，以后的录音里自动认出来。声音拿不准时会先问你。</p>
        </div>
      </header>
      <p className="vo-note"><ShieldCheck />声纹只在这台 NAS 上计算和保存，只有你自己能看到和使用；删除一个人会同时忘记 TA 的声纹。</p>
      {people === undefined ? <div className="vo-center"><Spinner /></div> : people.length === 0 ? (
        <div className="vo-empty">
          <UsersRound />
          <h3>还没有命名过任何人</h3>
          <p>打开一段转写好的录音，点击“发言人 1”这样的名字，输入 TA 是谁。</p>
        </div>
      ) : (
        <div className="vo-people-grid">
          {people.map((person, index) => {
            const appears = recordings.filter((recording) => recording.people.includes(person.name));
            return (
              <article key={person.id} className="vo-person">
                <header>
                  <span className="vo-mark named" style={{ width: 44, height: 44, fontSize: 19, background: voiceColor(index + 1) }}>{Array.from(person.name)[0]}</span>
                  {renaming === person.id ? (
                    <RenameForm person={person} onDone={(name) => {
                      setRenaming(undefined);
                      if (name && name !== person.name) void act(() => renamePerson(person.id, name), "已改名");
                    }} />
                  ) : (
                    <div className="vo-person-name">
                      <strong>{person.name}</strong>
                      <small>{person.samples ? `声纹来自 ${person.samples} 段发言` : "还没有声纹"}</small>
                    </div>
                  )}
                </header>
                <dl>
                  <div><dt>出现在</dt><dd>{person.recordings} 段录音</dd></div>
                  <div><dt>一共说了</dt><dd>{person.speechMs ? duration(person.speechMs) : "—"}</dd></div>
                </dl>
                {appears.length > 0 && (
                  <ul className="vo-person-recordings">
                    {appears.slice(0, 3).map((recording) => (
                      <li key={recording.id}><button type="button" onClick={() => onOpenRecording(recording.id)}><AudioLines />{recording.title}</button></li>
                    ))}
                  </ul>
                )}
                <footer>
                  <button type="button" className="vo-button small subtle" onClick={() => setRenaming(person.id)}><Pencil />改名</button>
                  {people.length > 1 && (
                    <Popover label="和另一个人合并" className="vo-button small subtle" align="start" trigger={<><GitMerge /><span>合并</span></>}>
                      {(close) => (
                        <div className="vo-menu" role="menu">
                          <p className="vo-menu-title">{person.name} 和谁是同一个人？</p>
                          {people.filter((other) => other.id !== person.id).map((other) => (
                            <button key={other.id} type="button" role="menuitem" onClick={() => { close(); void act(() => mergePeople(person.id, other.id), `已合并到${other.name}`); }}>{other.name}</button>
                          ))}
                        </div>
                      )}
                    </Popover>
                  )}
                  <button type="button" className="vo-button small subtle danger" onClick={() => setDeleting(person)}><Trash2 />删除</button>
                </footer>
              </article>
            );
          })}
        </div>
      )}
      {deleting && (
        <div className="vo-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) setDeleting(undefined); }}>
          <div className="vo-dialog small" role="alertdialog" aria-modal="true" aria-label="删除发言人">
            <h3>删除“{deleting.name}”？</h3>
            <p>会忘记 TA 的声纹，录音里命名为 TA 的发言人恢复为“发言人 N”。转写的内容不会删除。</p>
            <div className="vo-dialog-actions">
              <button type="button" className="vo-button" onClick={() => setDeleting(undefined)}>取消</button>
              <button type="button" className="vo-button danger" onClick={() => { const person = deleting; setDeleting(undefined); void act(() => deletePerson(person.id), `已删除${person.name}`); }}>删除</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function RenameForm({ person, onDone }: { person: Person; onDone: (name?: string) => void }) {
  const [name, setName] = useState(person.name);
  const submit = (event: FormEvent) => { event.preventDefault(); onDone(name.trim() || undefined); };
  return (
    <form className="vo-person-rename" onSubmit={submit}>
      <input value={name} maxLength={40} autoFocus aria-label="新名字" onChange={(event) => setName(event.target.value)} onBlur={() => onDone(name.trim() || undefined)}
        onKeyDown={(event) => { if (event.key === "Escape") onDone(undefined); }} />
    </form>
  );
}
