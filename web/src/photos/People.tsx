import { Check, ChevronDown, ChevronUp, EyeOff, PawPrint, Pencil, UserRound, X } from "lucide-react";
import { FormEvent, useEffect, useMemo, useState } from "react";

import { type Caller, libraryName, messageOf } from "./model";
import {
  assignFace,
  avatarURL,
  listPeople,
  type PhotoAsset,
  type PhotoAssetPerson,
  type PhotoLibrary,
  type PhotoPerson,
} from "./photosApi";

// People and pets (docs/architecture/photo-people.md): local AI finds the same
// face, cat or dog again across a library's photos, and members name, merge
// and correct who is who. Everything here is per library.

const kindOfFace: Record<PhotoAssetPerson["kind"], PhotoPerson["kind"]> = { face: "person", cat: "cat", dog: "dog" };

// personName is what a person is called on the page; nameless ones invite a name.
export const personName = (person: Pick<PhotoPerson, "name" | "kind">) =>
  person.name || (person.kind === "person" ? "未命名" : person.kind === "cat" ? "未命名的猫" : "未命名的狗");

export function Avatar({
  occurrenceId,
  kind,
  size = 40,
  className = "",
}: {
  occurrenceId?: string;
  kind: PhotoPerson["kind"];
  size?: number;
  className?: string;
}) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [occurrenceId]);
  return (
    <span className={`ph-avatar ${className}`} style={{ width: size, height: size }}>
      {occurrenceId && !failed ? (
        <img src={avatarURL(occurrenceId)} alt="" loading="lazy" draggable={false} onError={() => setFailed(true)} />
      ) : kind === "person" ? (
        <UserRound />
      ) : (
        <PawPrint />
      )}
    </span>
  );
}

// Avatars are at least this wide on AI 聚合; a collapsed row shows one line.
export const personWidth = 96;
export const personGap = 18;

interface RowProps {
  people: PhotoPerson[];
  perRow: number;
  expanded: boolean;
  showHidden: boolean;
  onExpand: (expanded: boolean) => void;
  onShowHidden: (shown: boolean) => void;
  onOpen: (person: PhotoPerson) => void;
}

// PeopleRow is AI 聚合's 人物与宠物: round faces, named people first. People
// the members hid stay out of it until asked for.
export function PeopleRow({ people, perRow, expanded, showHidden, onExpand, onShowHidden, onOpen }: RowProps) {
  const visible = people.filter((person) => !person.hidden);
  const hidden = people.filter((person) => person.hidden);
  const shown = expanded ? visible : visible.slice(0, perRow);
  return (
    <section className="ph-browse ph-people" aria-label="人物与宠物">
      <header>
        <h3>人物与宠物</h3>
      </header>
      <div className="ph-people-grid">
        {shown.map((person) => (
          <PersonCard key={person.id} person={person} onOpen={onOpen} />
        ))}
      </div>
      <div className="ph-people-more">
        {visible.length > perRow && (
          <button type="button" className="ph-browse-more" onClick={() => onExpand(!expanded)}>
            {expanded ? (
              <>
                <ChevronUp />
                收起
              </>
            ) : (
              <>
                <ChevronDown />
                展开全部 {visible.length} 位
              </>
            )}
          </button>
        )}
        {hidden.length > 0 && (
          <button type="button" className="ph-browse-more quiet" onClick={() => onShowHidden(!showHidden)}>
            <EyeOff />
            {showHidden ? "收起已隐藏的" : `已隐藏 ${hidden.length} 位`}
          </button>
        )}
      </div>
      {showHidden && hidden.length > 0 && (
        <div className="ph-people-grid hidden-people" aria-label="已隐藏的人物">
          {hidden.map((person) => (
            <PersonCard key={person.id} person={person} onOpen={onOpen} />
          ))}
        </div>
      )}
    </section>
  );
}

function PersonCard({ person, onOpen }: { person: PhotoPerson; onOpen: (person: PhotoPerson) => void }) {
  return (
    <button
      type="button"
      className={`ph-person${person.name ? "" : " nameless"}`}
      aria-label={`打开 ${personName(person)}`}
      onClick={() => onOpen(person)}
    >
      <Avatar occurrenceId={person.coverId} kind={person.kind} size={personWidth} />
      <strong>{person.name || (person.canChange ? "添加名字" : personName(person))}</strong>
      <small>{person.photos} 张</small>
    </button>
  );
}

// PeopleSettings turns people and pets on or off for each library the
// caller has: the owner of a private library decides, an administrator for
// the shared one.
export function PeopleSettings({
  caller,
  onSet,
  onClose,
}: {
  caller: Caller;
  onSet: (library: PhotoLibrary, on: boolean) => void;
  onClose: () => void;
}) {
  const libraries = caller.libraries.filter((library) => !library.viewing);
  return (
    <div
      className="ph-dialog-backdrop"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        className="ph-dialog ph-people-settings"
        role="dialog"
        aria-modal="true"
        aria-label="人物与宠物设置"
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
      >
        <header>
          <h3>人物与宠物</h3>
          <small>本地 AI 在这台 NAS 上认出反复出现的人、猫和狗，每个图库分别设置</small>
          <button type="button" className="ph-dialog-close" aria-label="关闭" onClick={onClose}>
            <X />
          </button>
        </header>
        <div className="ph-switch-list">
          {libraries.map((library) => {
            const on = library.people !== false;
            const label = libraryName(caller, library);
            return (
              <div className="ph-switch-row" key={library.id}>
                <span>
                  <strong>{label}</strong>
                  <small>
                    {library.canSetPeople
                      ? on
                        ? "已开启"
                        : "已关闭，不识别新照片"
                      : `${on ? "已开启" : "已关闭"} · 由管理员设置`}
                  </small>
                </span>
                <button
                  type="button"
                  role="switch"
                  aria-checked={on}
                  aria-label={`${label}的人物与宠物`}
                  disabled={!library.canSetPeople}
                  className={`ph-switch${on ? " on" : ""}`}
                  onClick={() => onSet(library, !on)}
                >
                  <span />
                </button>
              </div>
            );
          })}
        </div>
        <p className="ph-people-settings-note">
          关闭会删除这个图库里识别出的人脸与宠物；起的名字和做过的更正都会保留，重新开启后随识别结果回来。
        </p>
      </div>
    </div>
  );
}

// PersonPicker chooses who a person is merged into: someone of the same
// library and kind.
export function PersonPicker({
  person,
  viewing,
  onPick,
  onClose,
}: {
  person: PhotoPerson;
  viewing: string;
  onPick: (into: PhotoPerson) => void;
  onClose: () => void;
}) {
  const [people, setPeople] = useState<PhotoPerson[]>();
  const [filter, setFilter] = useState("");
  useEffect(() => {
    let live = true;
    listPeople(viewing, true).then(
      (found) => {
        if (live) setPeople(found);
      },
      () => {
        if (live) setPeople([]);
      },
    );
    return () => {
      live = false;
    };
  }, [viewing]);
  const candidates = (people ?? []).filter(
    (other) =>
      other.id !== person.id &&
      other.libraryId === person.libraryId &&
      other.kind === person.kind &&
      (!filter.trim() || other.name.includes(filter.trim())),
  );
  return (
    <div
      className="ph-dialog-backdrop"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        className="ph-dialog ph-picker ph-person-picker"
        role="dialog"
        aria-modal="true"
        aria-label="合并到"
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
      >
        <header>
          <h3>把{person.name ? `“${person.name}”` : "这个人"}合并到…</h3>
          <small>这些照片都会归到所选的人名下</small>
          <button type="button" className="ph-dialog-close" aria-label="关闭" onClick={onClose}>
            <X />
          </button>
        </header>
        <input
          className="ph-person-filter"
          autoFocus
          aria-label="按名字筛选"
          placeholder="按名字筛选"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
        />
        {people === undefined ? (
          <div className="ph-loading">
            <span className="ph-spinner" />
          </div>
        ) : (
          <div className="ph-picker-list">
            {candidates.map((other) => (
              <button type="button" key={other.id} className="ph-picker-item" onClick={() => onPick(other)}>
                <Avatar occurrenceId={other.coverId} kind={other.kind} size={34} />
                <span>{personName(other)}</span>
                <small>{other.photos} 张</small>
              </button>
            ))}
            {!candidates.length && (
              <p className="ph-muted">{filter.trim() ? "没有叫这个名字的人" : "这个图库里没有其他人可以合并"}</p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

interface FacesProps {
  asset: PhotoAsset;
  faces: PhotoAssetPerson[];
  editable: boolean;
  viewing: string;
  onChanged: (asset: PhotoAsset) => void;
  onOpenPerson: (personId: string) => void;
  onError: (message: string) => void;
}

// PhotoFaces lists who is in a photo in the viewer's details. A face nobody
// has named yet can be named on the spot; typing an existing name puts it
// with that person.
export function PhotoFaces({ asset, faces, editable, viewing, onChanged, onOpenPerson, onError }: FacesProps) {
  const [editing, setEditing] = useState<string>();
  const [name, setName] = useState("");
  const [people, setPeople] = useState<PhotoPerson[]>();
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!editing || people) return;
    let live = true;
    listPeople(viewing, true).then(
      (found) => {
        if (live) setPeople(found);
      },
      () => {
        if (live) setPeople([]);
      },
    );
    return () => {
      live = false;
    };
  }, [editing]);
  const face = faces.find((item) => item.occurrenceId === editing);
  const suggestions = useMemo(
    () =>
      !face
        ? []
        : (people ?? [])
            .filter(
              (person) =>
                person.libraryId === asset.libraryId &&
                person.kind === kindOfFace[face.kind] &&
                person.name &&
                person.name.includes(name.trim()) &&
                person.id !== face.personId,
            )
            .slice(0, 6),
    [people, face, name],
  );
  const start = (item: PhotoAssetPerson) => {
    setEditing(item.occurrenceId);
    setName("");
  };
  const assign = async (who: { personId: string } | { name: string }) => {
    if (!face) return;
    setSaving(true);
    try {
      onChanged(await assignFace(asset.id, face.occurrenceId, who));
      setEditing(undefined);
    } catch (caught) {
      onError(messageOf(caught));
    } finally {
      setSaving(false);
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const typed = name.trim();
    if (!typed || !face) return;
    const same = (people ?? []).find(
      (person) =>
        person.libraryId === asset.libraryId && person.kind === kindOfFace[face.kind] && person.name === typed,
    );
    void assign(same ? { personId: same.id } : { name: typed });
  };
  if (!faces.length) return null;
  return (
    <section className="ph-faces" aria-label="照片中的人物">
      <h4>照片中的人物</h4>
      <ul>
        {faces.map((item) => {
          const kind = kindOfFace[item.kind];
          const open = editing === item.occurrenceId;
          return (
            <li key={item.occurrenceId} className={open ? "editing" : ""}>
              <Avatar occurrenceId={item.occurrenceId} kind={kind} size={36} />
              {open ? (
                <form onSubmit={submit}>
                  <input
                    autoFocus
                    aria-label="名字"
                    placeholder={kind === "person" ? "这是谁？" : "它叫什么？"}
                    maxLength={40}
                    value={name}
                    disabled={saving}
                    onChange={(event) => setName(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Escape") {
                        event.stopPropagation();
                        setEditing(undefined);
                      }
                    }}
                  />
                  <button type="submit" aria-label="保存名字" disabled={!name.trim() || saving}>
                    {saving ? <span className="ph-spinner" /> : <Check />}
                  </button>
                  {suggestions.length > 0 && (
                    <div className="ph-face-suggestions" role="listbox" aria-label="已有的人">
                      {suggestions.map((person) => (
                        <button
                          type="button"
                          role="option"
                          aria-selected="false"
                          key={person.id}
                          disabled={saving}
                          onClick={() => void assign({ personId: person.id })}
                        >
                          <Avatar occurrenceId={person.coverId} kind={person.kind} size={22} />
                          {person.name}
                          <small>{person.photos} 张</small>
                        </button>
                      ))}
                    </div>
                  )}
                </form>
              ) : item.personId && item.name ? (
                <>
                  <button
                    type="button"
                    className="ph-face-name"
                    title="查看 TA 的全部照片"
                    onClick={() => onOpenPerson(item.personId!)}
                  >
                    {item.name}
                  </button>
                  {editable && (
                    <button
                      type="button"
                      className="ph-face-edit"
                      aria-label={`不是${item.name}？改成别人`}
                      title="不是 TA？改成别人"
                      onClick={() => start(item)}
                    >
                      <Pencil />
                    </button>
                  )}
                </>
              ) : item.personId ? (
                <>
                  <button
                    type="button"
                    className="ph-face-name quiet"
                    title="查看 TA 的全部照片"
                    onClick={() => onOpenPerson(item.personId!)}
                  >
                    {personName({ name: "", kind })}
                  </button>
                  {editable && (
                    <button
                      type="button"
                      className="ph-face-edit"
                      aria-label="添加名字"
                      title="添加名字"
                      onClick={() => start(item)}
                    >
                      <Pencil />
                    </button>
                  )}
                </>
              ) : editable ? (
                <button type="button" className="ph-face-name add" onClick={() => start(item)}>
                  {kind === "person" ? "这是谁？" : "添加名字"}
                </button>
              ) : (
                <span className="ph-face-name quiet">{kind === "person" ? "未认出" : "未命名"}</span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
