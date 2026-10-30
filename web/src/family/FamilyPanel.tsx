import { Check, Plus, RotateCcw, Trash2 } from "lucide-react";
import { MotionConfig } from "motion/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { relativeTime } from "../lib/time";
import { useDialogs, useToasts } from "../ui/feedback";
import {
  cancelJoin,
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
import { Modal } from "./parts";

interface Props {
  userId: string;
  onShowPhoto: (assetId: string) => void;
}

// FamilyPanel is the 家庭中心 window. Accounts outside the family see how to
// join; members pick a baby in the narrow rail and read the channel beside it.
export function FamilyPanel({ userId, onShowPhoto }: Props) {
  const [overview, setOverview] = useState<FamilyOverview>();
  const [catalog, setCatalog] = useState<FamilyCatalog>();
  const [failure, setFailure] = useState("");
  const [selected, setSelected] = useState<string>();
  const [adding, setAdding] = useState(false);
  const [restoring, setRestoring] = useState(false);
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
          <button type="button" className={styles.fcSecondary} onClick={() => void reload()}>
            重试
          </button>
        </div>
      </div>
    );
  }
  if (!overview || !catalog || !context) {
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
      {/* Motion follows the system: with reduced motion, things change in place. */}
      <MotionConfig reducedMotion="user">
        <div className={`${styles.fcApp} ${styles.fcLayout}`} ref={root}>
          <nav className={styles.fcRail} aria-label="宝宝">
            {overview.babies.map((item) => (
              <button
                key={item.id}
                type="button"
                className={`${styles.fcRailBaby} ${item.id === selected ? styles.active : ""}`}
                aria-current={item.id === selected ? "page" : undefined}
                onClick={() => setSelected(item.id)}
              >
                <BabyAvatar baby={item} className={styles.fcRailFace} />
                <span>{item.nickname || item.name}</span>
              </button>
            ))}
            {overview.me.guardian && (
              <button type="button" className={styles.fcRailAdd} onClick={() => setAdding(true)} aria-label="添加宝宝">
                <Plus size={18} />
              </button>
            )}
            {overview.deletedBabies.length > 0 && (
              <button
                type="button"
                className={styles.fcRailTrash}
                onClick={() => setRestoring(true)}
                aria-label="最近删除的宝宝"
                title="最近删除的宝宝"
              >
                <Trash2 size={16} />
              </button>
            )}
          </nav>
          {baby ? (
            <Channel key={baby.id} baby={baby} />
          ) : (
            <NoBabies guardian={overview.me.guardian} onAdd={() => setAdding(true)} />
          )}
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
          {restoring && (
            <Modal title="最近删除的宝宝" onClose={() => setRestoring(false)}>
              <p className={styles.fcNote}>删除的频道保留 15 天，恢复后记录、录音和留言都会回来。</p>
              <ul className={styles.fcRows}>
                {overview.deletedBabies.map((deleted) => (
                  <li key={deleted.id}>
                    <BabyAvatar baby={deleted} className={styles.fcFace} />
                    <span className={styles.fcRowMain}>
                      {deleted.name}
                      <small>{deleted.deletedAt ? `删除于${relativeTime(deleted.deletedAt)}` : ""}</small>
                    </span>
                    <button
                      type="button"
                      className={styles.fcSecondary}
                      onClick={async () => {
                        try {
                          await restoreBaby(deleted.id);
                          await reload();
                          setSelected(deleted.id);
                          setRestoring(false);
                          toast({ text: `已恢复${deleted.name}的频道`, tone: "done" });
                        } catch (caught) {
                          toast({ text: familyError(caught), tone: "error" });
                        }
                      }}
                    >
                      <RotateCcw size={13} /> 恢复
                    </button>
                  </li>
                ))}
              </ul>
            </Modal>
          )}
          {dialog}
          {toasts}
        </div>
      </MotionConfig>
    </FamilyContext>
  );
}

function NoBabies({ guardian, onAdd }: { guardian: boolean; onAdd: () => void }) {
  return (
    <div className={styles.fcBlank}>
      <h2>还没有宝宝</h2>
      {!guardian && <p>家长添加宝宝后会显示在这里。</p>}
      {guardian && (
        <button type="button" className={styles.fcPrimary} onClick={onAdd}>
          添加宝宝
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
        {pending ? (
          <>
            <h1>已申请加入</h1>
            <p>你申请以“{overview.me.role}”的身份加入家庭中心。</p>
            <p className={styles.fcWaiting}>等待家长批准</p>
            <button type="button" className={styles.fcLink} onClick={() => void onCancel()}>
              撤回申请
            </button>
          </>
        ) : (
          <>
            <h1>{overview.exists ? "加入家庭中心" : "创建家庭中心"}</h1>
            <p>
              {overview.exists
                ? "选择你在家里的称呼，家长同意后加入。"
                : "选择你在家里的称呼。创建家庭的人成为家长，可以添加宝宝、审批其他家人加入。"}
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
                placeholder="舅舅、姑姑、小姨…"
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
            <p className={styles.fcNote}>宝宝的照片视频存进共享图库，NAS 上的每个账号都能在相册里看到。</p>
          </>
        )}
      </div>
    </div>
  );
}
