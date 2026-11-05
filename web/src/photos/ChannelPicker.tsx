import { Check, X } from "lucide-react";
import { Dialog } from "radix-ui";
import { useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { addRecord, babyAvatarURL, type FamilyBaby, familyError, requestKey } from "../family/api";
import { batchByDay, dayOf, inAlbum, needsCopy, sharedIdOf, toShared } from "../family/fromLibrary";
import { initial } from "../people/PersonAvatar";
import feedbackStyles from "../ui/feedback.module.css";
import type { PhotoAsset } from "./api";
import { assetsLabel } from "./model";
import styles from "./photos.module.css";

interface Props {
  assets: PhotoAsset[];
  babies: FamilyBaby[];
  sharedLibraryId: string;
  onDone: (added: FamilyBaby[]) => void;
  onClose: () => void;
}

// ChannelPicker adds photos to babies' channels in 家庭中心: as one record per
// day they were taken, the way the family center adds them. Photos of the
// member's own library join by their shared copy, made now when there is
// none (ADR 0029); each channel leaves out the photos it has already.
export function ChannelPicker({ assets, babies, sharedLibraryId, onDone, onClose }: Props) {
  const [chosen, setChosen] = useState<string[]>(() => (babies.length === 1 ? [babies[0].id] : []));
  // Each baby's album: which of the photos it has; undefined until asked.
  const [has, setHas] = useState<ReadonlyMap<string, ReadonlySet<string>>>();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState<string>();
  const [problem, setProblem] = useState("");
  // Another try keeps the copies made and the keys that name each request.
  const copies = useRef(new Map<string, string>());
  const keys = useRef(new Map<string, string>());

  useEffect(() => {
    let live = true;
    const ids = assets.map((asset) => sharedIdOf(asset, sharedLibraryId)).filter((id): id is string => Boolean(id));
    Promise.all(babies.map(async (baby) => [baby.id, await inAlbum(baby.id, ids)] as const)).then(
      (found) => {
        if (live) setHas(new Map(found));
      },
      // Saving leaves them out anyway.
      () => {
        if (live) setHas(new Map());
      },
    );
    return () => {
      live = false;
    };
  }, [assets, babies, sharedLibraryId]);

  const present = (baby: FamilyBaby) =>
    assets.filter((asset) => {
      const id = sharedIdOf(asset, sharedLibraryId);
      return id !== undefined && has?.get(baby.id)?.has(id);
    }).length;
  const copying = needsCopy(assets, sharedLibraryId);
  const targets = babies.filter((baby) => chosen.includes(baby.id) && present(baby) < assets.length);

  const save = async () => {
    setProblem("");
    try {
      setBusy("正在复制到共享图库");
      const ids = await toShared(assets, sharedLibraryId, copies.current, (done) =>
        setBusy(`正在复制到共享图库 ${done}/${copying}`),
      );
      setBusy("正在加入");
      const photos = [...new Map(assets.map((asset, i) => [ids[i], { id: ids[i], day: dayOf(asset) }])).values()];
      const added: FamilyBaby[] = [];
      for (const baby of targets) {
        const skip = has?.get(baby.id);
        const batches = batchByDay(photos.filter((photo) => !skip?.has(photo.id)));
        for (const [index, batch] of batches.entries()) {
          const name = `${baby.id}:${index}`;
          if (!keys.current.has(name)) keys.current.set(name, requestKey());
          try {
            await addRecord(baby.id, {
              kind: "photos",
              photos: batch,
              text: text.trim(),
              requestKey: keys.current.get(name),
            });
          } catch (caught) {
            if (!(caught instanceof APIError && caught.code === "already_in_channel")) throw caught;
          }
        }
        added.push(baby);
      }
      onDone(added);
    } catch (caught) {
      setProblem(familyError(caught));
      setBusy(undefined);
    }
  };

  return (
    <Dialog.Root
      open
      modal={false}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <div
        className={feedbackStyles.phDialogBackdrop}
        onPointerDown={(event) => {
          if (event.target === event.currentTarget && !busy) onClose();
        }}
      >
        <Dialog.Content
          className={`${feedbackStyles.phDialog} ${styles.phPicker}`}
          aria-describedby={undefined}
          onInteractOutside={(event) => event.preventDefault()}
        >
          <header>
            <Dialog.Title asChild>
              <h3>加入宝宝频道</h3>
            </Dialog.Title>
            <small>{assetsLabel(assets)}</small>
            <Dialog.Close className={feedbackStyles.phDialogClose} aria-label="关闭" disabled={Boolean(busy)}>
              <X />
            </Dialog.Close>
          </header>
          {has === undefined ? (
            <div className={styles.phLoading}>
              <span className={styles.phSpinner} />
            </div>
          ) : (
            <div className={styles.phPickerList} role="group" aria-label="宝宝">
              {babies.map((baby) => {
                const already = present(baby);
                const full = already === assets.length;
                const on = chosen.includes(baby.id) && !full;
                return (
                  <button
                    type="button"
                    key={baby.id}
                    role="checkbox"
                    aria-checked={on}
                    disabled={full || Boolean(busy)}
                    className={`${styles.phPickerItem} ${styles.phChannelBaby}${on ? ` ${styles.on}` : ""}`}
                    onClick={() =>
                      setChosen((current) =>
                        current.includes(baby.id) ? current.filter((id) => id !== baby.id) : [...current, baby.id],
                      )
                    }
                  >
                    <span className={styles.phChannelFace} aria-hidden="true">
                      {baby.avatarVersion > 0 ? (
                        <img src={babyAvatarURL(baby)} alt="" draggable={false} />
                      ) : (
                        initial(baby.nickname || baby.name)
                      )}
                    </span>
                    <span>{baby.nickname || baby.name}</span>
                    <small>{full ? "都已在频道中" : already ? `${already} 张已在频道中，会跳过` : ""}</small>
                    <span className={styles.phChannelCheck}>
                      <Check strokeWidth={3} />
                    </span>
                  </button>
                );
              })}
            </div>
          )}
          <label>
            说明（可选）
            <input
              value={text}
              maxLength={500}
              onChange={(event) => setText(event.target.value)}
              disabled={Boolean(busy)}
            />
          </label>
          <p className={styles.phMuted}>
            {copying ? `其中 ${copying} 张在我的图库，会先复制到共享图库，家人都能看到。` : ""}
            拍摄日期不同的照片按天分成多条记录。
          </p>
          {problem && <p className={styles.phChannelProblem}>{problem}</p>}
          <footer className={feedbackStyles.phDialogActions}>
            {busy && <span className={styles.phChannelBusy}>{busy}</span>}
            <button type="button" onClick={onClose} disabled={Boolean(busy)}>
              取消
            </button>
            <button
              type="button"
              className="primary"
              disabled={!targets.length || Boolean(busy) || has === undefined}
              onClick={() => void save()}
            >
              加入
            </button>
          </footer>
        </Dialog.Content>
      </div>
    </Dialog.Root>
  );
}
