import { Baby, Check, Clock3, HeartHandshake, Plus, RotateCcw, Users } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { PersonAvatar } from "../people/PersonAvatar";
import { person, usePeople } from "../people/people";
import { useDialogs, useToasts } from "../ui/feedback";
import {
  cancelJoin,
  type FamilyBaby,
  type FamilyCatalog,
  type FamilyOverview,
  familyError,
  getCatalog,
  getFamily,
  joinFamily,
  restoreBaby,
} from "./api";
import { BabyAvatar, BabyDialog } from "./BabyDialog";
import { Channel } from "./Channel";
import { FamilyContext } from "./context";
import styles from "./family.module.css";
import { ageText } from "./format";

interface Props {
  userId: string;
  onShowPhoto: (assetId: string) => void;
}

// FamilyPanel is the 家庭中心 window. Accounts outside the family see how to
// join; members pick a baby on the left and read the channel on the right.
export function FamilyPanel({ userId, onShowPhoto }: Props) {
  const [overview, setOverview] = useState<FamilyOverview>();
  const [catalog, setCatalog] = useState<FamilyCatalog>();
  const [failure, setFailure] = useState("");
  const [selected, setSelected] = useState<string>();
  const [adding, setAdding] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const { prompt, confirm, dialog } = useDialogs();
  const { toast, element: toasts } = useToasts();
  const reload = useCallback(async () => {
    try {
      const next = await getFamily();
      setOverview(next);
      setFailure("");
      setSelected((current) =>
        next.babies.some((baby) => baby.id === current) ? current : (next.babies[0]?.id ?? undefined),
      );
      return next;
    } catch (caught) {
      setFailure(familyError(caught));
      return undefined;
    }
  }, []);

  // The window's parts read the family through context; a new value only
  // when the family or the catalog changes keeps them from reading again.
  const context = useMemo(
    () =>
      overview && catalog
        ? { userId, overview, catalog, reload, toast, prompt, confirm, container: root, onShowPhoto }
        : undefined,
    [userId, overview, catalog, reload, toast, prompt, confirm, onShowPhoto],
  );

  useEffect(() => {
    let live = true;
    Promise.all([getFamily(), getCatalog()]).then(
      ([family, shipped]) => {
        if (!live) return;
        setOverview(family);
        setCatalog(shipped);
        setSelected(family.babies[0]?.id);
      },
      (caught: unknown) => {
        if (live) setFailure(familyError(caught));
      },
    );
    return () => {
      live = false;
    };
  }, []);

  if (failure && !overview) {
    return (
      <div className={styles.fcApp} ref={root}>
        <div className={styles.fcCentered}>
          <p className={styles.fcFailure}>{failure}</p>
          <button type="button" className="primary" onClick={() => void reload()}>
            重试
          </button>
        </div>
      </div>
    );
  }
  if (!overview || !catalog) {
    return (
      <div className={styles.fcApp} ref={root}>
        <div className={styles.fcCentered}>
          <span className="loader" aria-label="正在打开家庭中心" />
        </div>
      </div>
    );
  }
  if (overview.me.status !== "member") {
    return (
      <div className={styles.fcApp} ref={root}>
        <JoinScreen
          overview={overview}
          roles={catalog.roles}
          onJoin={async (role) => {
            try {
              await joinFamily(role);
              await reload();
            } catch (caught) {
              toast({ text: familyError(caught), tone: "error" });
            }
          }}
          onCancel={async () => {
            try {
              await cancelJoin();
              await reload();
            } catch (caught) {
              toast({ text: familyError(caught), tone: "error" });
            }
          }}
        />
        {toasts}
      </div>
    );
  }

  const baby = overview.babies.find((item) => item.id === selected);
  return (
    <FamilyContext value={context}>
      <div className={`${styles.fcApp} ${styles.fcSplit}`} ref={root}>
        <Sidebar
          userId={userId}
          overview={overview}
          selected={selected}
          onSelect={setSelected}
          onAdd={() => setAdding(true)}
          onRestore={async (deleted) => {
            try {
              await restoreBaby(deleted.id);
              await reload();
              setSelected(deleted.id);
              toast({ text: `已恢复${deleted.name}的频道`, tone: "done" });
            } catch (caught) {
              toast({ text: familyError(caught), tone: "error" });
            }
          }}
        />
        <main className={styles.fcMain}>
          {baby ? (
            <Channel key={baby.id} baby={baby} />
          ) : (
            <NoBabies guardian={overview.me.guardian} onAdd={() => setAdding(true)} />
          )}
        </main>
        {adding && (
          <BabyDialog
            onClose={() => setAdding(false)}
            onSaved={async (saved) => {
              setAdding(false);
              await reload();
              setSelected(saved.id);
            }}
          />
        )}
        {dialog}
        {toasts}
      </div>
    </FamilyContext>
  );
}

function Sidebar({
  userId,
  overview,
  selected,
  onSelect,
  onAdd,
  onRestore,
}: {
  userId: string;
  overview: FamilyOverview;
  selected?: string;
  onSelect: (id: string) => void;
  onAdd: () => void;
  onRestore: (baby: FamilyBaby) => void;
}) {
  const people = usePeople();
  const me = overview.members.find((member) => member.accountId === userId);
  return (
    <aside className={styles.fcSide}>
      <header className={styles.fcSideHead}>
        <span className={styles.fcSideMark} aria-hidden="true">
          <HeartHandshake size={18} />
        </span>
        <div>
          <strong>家庭中心</strong>
          <small>{overview.members.length} 位家人</small>
        </div>
      </header>
      <nav aria-label="宝宝" className={styles.fcBabyList}>
        <span className={styles.fcSideLabel}>宝宝</span>
        {overview.babies.map((baby) => (
          <button
            key={baby.id}
            type="button"
            className={`${styles.fcBabyItem} ${baby.id === selected ? styles.active : ""}`}
            aria-current={baby.id === selected ? "page" : undefined}
            onClick={() => onSelect(baby.id)}
          >
            <BabyAvatar baby={baby} className={styles.fcBabyItemFace} />
            <span>
              <strong>{baby.nickname || baby.name}</strong>
              <small>{ageText(baby.age, false)}</small>
            </span>
          </button>
        ))}
        {overview.me.guardian && (
          <button type="button" className={styles.fcAddBaby} onClick={onAdd}>
            <Plus size={15} /> 添加宝宝
          </button>
        )}
      </nav>
      {overview.deletedBabies.length > 0 && (
        <div className={styles.fcDeleted}>
          <span className={styles.fcSideLabel}>最近删除</span>
          {overview.deletedBabies.map((baby) => (
            <div key={baby.id} className={styles.fcDeletedItem}>
              <span>{baby.name}</span>
              <button type="button" onClick={() => onRestore(baby)} aria-label={`恢复${baby.name}的频道`}>
                <RotateCcw size={13} /> 恢复
              </button>
            </div>
          ))}
        </div>
      )}
      <footer className={styles.fcMe}>
        {me && (
          <PersonAvatar who={person(people, { id: me.accountId, username: me.username })} className={styles.fcMeFace} />
        )}
        <span>
          <strong>我是{overview.me.role}</strong>
          <small>{overview.me.guardian ? "家长" : "亲友"}</small>
        </span>
      </footer>
    </aside>
  );
}

function NoBabies({ guardian, onAdd }: { guardian: boolean; onAdd: () => void }) {
  return (
    <div className={styles.fcEmptyChannel}>
      <span className={styles.fcEmptyIcon} aria-hidden="true">
        <Baby size={34} />
      </span>
      <h2>{guardian ? "添加第一个宝宝" : "还没有宝宝"}</h2>
      <p>
        {guardian
          ? "每个宝宝有自己的频道：照片视频、录音、日志、身高体重、疫苗和大事记都记在这里，全家一起看。"
          : "家长添加宝宝后，这里会出现宝宝的频道。"}
      </p>
      {guardian && (
        <button type="button" className={styles.fcPrimary} onClick={onAdd}>
          <Plus size={16} /> 添加宝宝
        </button>
      )}
    </div>
  );
}

function JoinScreen({
  overview,
  roles,
  onJoin,
  onCancel,
}: {
  overview: FamilyOverview;
  roles: string[];
  onJoin: (role: string) => Promise<void>;
  onCancel: () => Promise<void>;
}) {
  const [role, setRole] = useState("");
  const [custom, setCustom] = useState("");
  const [busy, setBusy] = useState(false);
  const chosen = role === "其他" ? custom.trim() : role;
  const pending = overview.me.status === "pending";
  const submit = async () => {
    if (!chosen || busy) return;
    setBusy(true);
    await onJoin(chosen);
    setBusy(false);
  };
  return (
    <div className={styles.fcJoin}>
      <div className={styles.fcJoinCard}>
        <span className={styles.fcJoinBadge} aria-hidden="true">
          <Users size={28} />
        </span>
        {pending ? (
          <>
            <h1>已申请加入</h1>
            <p>你申请以“{overview.me.role}”的身份加入家庭。家长批准后，你就能看到宝宝的频道， 和家人一起记录。</p>
            <div className={styles.fcPending}>
              <Clock3 size={16} /> 等待家长批准
            </div>
            <button type="button" className={styles.fcLink} onClick={() => void onCancel()}>
              撤回申请
            </button>
          </>
        ) : (
          <>
            <h1>{overview.exists ? "加入家庭中心" : "创建家庭中心"}</h1>
            <p>
              {overview.exists
                ? "选择你在家里的称呼。家长批准后，你就能看到宝宝的频道，上传照片、写日志、留言。"
                : "你是第一位家人。选择你的称呼来创建家庭，你会成为家长，可以添加宝宝、批准其他家人加入。"}
            </p>
            <div className={styles.fcRoles} role="radiogroup" aria-label="我在家里是">
              {[...roles, "其他"].map((item) => (
                <button
                  key={item}
                  type="button"
                  role="radio"
                  aria-checked={role === item}
                  className={`${styles.fcRole} ${role === item ? styles.active : ""}`}
                  onClick={() => setRole(item)}
                >
                  {role === item && <Check size={13} />}
                  {item}
                </button>
              ))}
            </div>
            {role === "其他" && (
              <input
                className={styles.fcInput}
                placeholder="例如：舅舅、姑姑、小姨"
                maxLength={8}
                value={custom}
                onChange={(event) => setCustom(event.target.value)}
                aria-label="自己填写称呼"
                autoFocus
              />
            )}
            <button type="button" className={styles.fcPrimary} disabled={!chosen || busy} onClick={() => void submit()}>
              {overview.exists ? "申请加入" : "创建家庭"}
            </button>
            <p className={styles.fcFootnote}>宝宝的照片视频存进共享图库，NAS 上的每个账号都能在相册里看到。</p>
          </>
        )}
      </div>
    </div>
  );
}
