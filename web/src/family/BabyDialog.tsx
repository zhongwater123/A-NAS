import { Camera, Trash2 } from "lucide-react";
import { useRef, useState } from "react";

import { initial } from "../people/PersonAvatar";
import {
  addBaby,
  babyAvatarURL,
  deleteBaby,
  type FamilyBaby,
  familyError,
  removeBabyAvatar,
  setBabyAvatar,
  updateBaby,
} from "./api";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { today } from "./format";
import { Field, Modal } from "./parts";

// BabyAvatar shows a baby's picture, or on a warm disc the character a text
// avatar shows for the nickname or name (陈小禾 → 禾).
export function BabyAvatar({ baby, className }: { baby: FamilyBaby; className: string }) {
  const [failed, setFailed] = useState(0);
  const pictured = baby.avatarVersion > 0 && failed !== baby.avatarVersion;
  return (
    <span className={`${className} ${styles.fcBabyFace}`} aria-hidden="true">
      {pictured ? (
        <img src={babyAvatarURL(baby)} alt="" draggable={false} onError={() => setFailed(baby.avatarVersion)} />
      ) : (
        initial(baby.nickname || baby.name)
      )}
    </span>
  );
}

// BabyDialog adds a baby, or edits one for a guardian: the profile, the
// picture, and deleting the channel.
export function BabyDialog({
  baby,
  onClose,
  onSaved,
}: {
  baby?: FamilyBaby;
  onClose: () => void;
  onSaved: (baby: FamilyBaby) => void | Promise<void>;
}) {
  const family = useFamily();
  const [name, setName] = useState(baby?.name ?? "");
  const [nickname, setNickname] = useState(baby?.nickname ?? "");
  const [sex, setSex] = useState<"female" | "male" | "">(baby?.sex ?? "");
  const [birthDate, setBirthDate] = useState(baby?.birthDate ?? "");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const [current, setCurrent] = useState(baby);
  const picker = useRef<HTMLInputElement>(null);
  const ready = name.trim() && sex && birthDate;

  const save = async () => {
    if (!ready || busy || !sex) return;
    setBusy(true);
    setProblem("");
    try {
      const fields = { name: name.trim(), nickname: nickname.trim(), sex, birthDate, version: current?.version ?? 0 };
      const saved = current ? await updateBaby(current.id, fields) : await addBaby(fields);
      await onSaved(saved);
    } catch (caught) {
      setProblem(familyError(caught));
      setBusy(false);
    }
  };

  const changePicture = async (file?: File) => {
    if (!file || !current) return;
    setBusy(true);
    setProblem("");
    try {
      setCurrent(await setBabyAvatar(current.id, file));
      void family.reload();
    } catch (caught) {
      setProblem(familyError(caught));
    }
    setBusy(false);
  };

  const removePicture = async () => {
    if (!current) return;
    setBusy(true);
    try {
      setCurrent(await removeBabyAvatar(current.id));
      void family.reload();
    } catch (caught) {
      setProblem(familyError(caught));
    }
    setBusy(false);
  };

  const remove = async () => {
    if (!current) return;
    const typed = await family.prompt({
      title: `删除${current.name}的频道`,
      label: `频道里的记录和录音会在“最近删除”里保留 15 天，照片视频仍在共享图库。请输入宝宝的姓名“${current.name}”确认。`,
      confirm: "删除",
      maxLength: 20,
    });
    if (typed === undefined) return;
    if (typed.trim() !== current.name) {
      setProblem("输入的姓名不一致，没有删除。");
      return;
    }
    try {
      await deleteBaby(current.id, typed.trim());
      onClose();
      await family.reload();
      family.toast({ text: `已删除${current.name}的频道，15 天内可以在左栏底部的“最近删除”中恢复` });
    } catch (caught) {
      setProblem(familyError(caught));
    }
  };

  return (
    <Modal
      title={current ? "宝宝资料" : "添加宝宝"}
      busy={busy}
      onClose={onClose}
      footer={
        <>
          {current && (
            <button type="button" className={styles.fcDangerLink} onClick={() => void remove()} disabled={busy}>
              <Trash2 size={14} /> 删除频道
            </button>
          )}
          <span className={styles.fcGrow} />
          <button type="button" className={styles.fcSecondary} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button type="button" className={styles.fcPrimary} onClick={() => void save()} disabled={!ready || busy}>
            {current ? "保存" : "添加"}
          </button>
        </>
      }
    >
      {current && (
        <div className={styles.fcAvatarEdit}>
          <BabyAvatar baby={current} className={styles.fcAvatarLarge} />
          <div>
            <button
              type="button"
              className={styles.fcSecondary}
              onClick={() => picker.current?.click()}
              disabled={busy}
            >
              <Camera size={14} /> 更换头像
            </button>
            {current.avatarVersion > 0 && (
              <button type="button" className={styles.fcLink} onClick={() => void removePicture()} disabled={busy}>
                移除头像
              </button>
            )}
          </div>
          <input
            ref={picker}
            type="file"
            accept="image/jpeg,image/png,image/gif,image/webp"
            hidden
            onChange={(event) => {
              void changePicture(event.target.files?.[0]);
              event.target.value = "";
            }}
          />
        </div>
      )}
      <div className={styles.fcFormRow}>
        <Field label="姓名">
          <input
            className={styles.fcInput}
            value={name}
            maxLength={20}
            onChange={(event) => setName(event.target.value)}
            autoFocus={!current}
          />
        </Field>
        <Field label="小名（可选）">
          <input
            className={styles.fcInput}
            value={nickname}
            maxLength={20}
            onChange={(event) => setNickname(event.target.value)}
          />
        </Field>
      </div>
      <div className={styles.fcFormRow}>
        <Field label="出生日">
          <input
            className={styles.fcInput}
            type="date"
            value={birthDate}
            max={today()}
            onChange={(event) => setBirthDate(event.target.value)}
          />
        </Field>
        <div className={styles.fcField}>
          <span className={styles.fcFieldLabel} id="fc-sex">
            性别
          </span>
          <div className={styles.fcSegment} role="radiogroup" aria-labelledby="fc-sex">
            {(
              [
                ["female", "女孩"],
                ["male", "男孩"],
              ] as const
            ).map(([value, label]) => (
              <button
                key={value}
                type="button"
                role="radio"
                aria-checked={sex === value}
                className={sex === value ? styles.active : ""}
                onClick={() => setSex(value)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
      </div>
      <p className={styles.fcFootnote}>性别和出生日用于成长曲线与疫苗程序。孩子长大后可以用自己的账号认领这份档案。</p>
      {problem && <p className={styles.fcProblem}>{problem}</p>}
    </Modal>
  );
}
