import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Trash2 } from "lucide-react";
import { useState } from "react";

import { errorText } from "../api/errorMessages";
import {
  type EinkPairedScreen,
  type EinkScreen,
  listScreens,
  lowBattery,
  overdueAfterMs,
  pairScreen,
  removeScreen,
  renameScreen,
} from "../eink/api";
import { relativeTime } from "../lib/time";
import { Banner, Field, Group, Modal, PageHeader, Pill, Row, RowMenu, useSettingsKit } from "./parts";
import styles from "./settings.module.css";

const queryKey = ["eink-screens"];

// EinkSection is Settings → 墨水屏, for administrators
// (docs/specs/eink-screens.md, “配对一块屏”): the paired screens, what each
// last said about itself, and pairing, renaming and removing them.
export function EinkSection() {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: 1 } } }));
  return (
    <QueryClientProvider client={client}>
      <Screens />
    </QueryClientProvider>
  );
}

function Screens() {
  const cache = useQueryClient();
  const { toast } = useSettingsKit();
  const screens = useQuery({ queryKey, queryFn: ({ signal }) => listScreens(signal), refetchInterval: 60_000 });
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<EinkScreen>();
  const [removing, setRemoving] = useState<EinkScreen>();
  const refresh = () => cache.invalidateQueries({ queryKey });
  const remove = useMutation({
    mutationFn: (screen: EinkScreen) => removeScreen(screen.id),
    onSuccess: (_, screen) => {
      setRemoving(undefined);
      toast({ text: `已移除“${screen.name}”，它的令牌不再有效。` });
      void refresh();
    },
    onError: (error) => toast({ text: errorText(error, "eink"), tone: "error" }),
  });
  return (
    <>
      <PageHeader
        title="墨水屏"
        action={
          <button type="button" className={`${styles.setBtn} primary`} onClick={() => setAdding(true)}>
            添加墨水屏
          </button>
        }
      />
      {screens.isError && <Banner tone="error">{errorText(screens.error, "eink")}</Banner>}
      <Group title="已配对的屏" anchor="eink-screens">
        {screens.data?.length === 0 && <div className={styles.setRow}>还没有配对的墨水屏</div>}
        {screens.data?.map((screen) => (
          <Row key={screen.id} label={screen.name} detail={describe(screen)}>
            {overdue(screen) && <Pill tone="warning">该来没来</Pill>}
            {(screen.report?.battery ?? 100) <= lowBattery && <Pill tone="danger">该充电了</Pill>}
            <RowMenu
              label={`更多操作：${screen.name}`}
              items={[
                { label: "改名", icon: Pencil, onSelect: () => setRenaming(screen) },
                { label: "移除", icon: Trash2, danger: true, onSelect: () => setRemoving(screen) },
              ]}
            />
          </Row>
        ))}
      </Group>
      {adding && <PairDialog onClose={() => setAdding(false)} onPaired={() => void refresh()} />}
      {renaming && (
        <RenameDialog screen={renaming} onClose={() => setRenaming(undefined)} onRenamed={() => void refresh()} />
      )}
      {removing && (
        <Modal title={`移除“${removing.name}”？`} busy={remove.isPending} onClose={() => setRemoving(undefined)}>
          <div className={styles.setDialogForm}>
            <p>它的令牌马上失效，屏连不上满 1 小时会在左下角贴上“停止更新”。要再用就重新添加。</p>
            <footer>
              <button
                type="button"
                className={styles.setBtn}
                disabled={remove.isPending}
                onClick={() => setRemoving(undefined)}
              >
                取消
              </button>
              <button
                type="button"
                className={`${styles.setBtn} primary ${styles.danger}`}
                disabled={remove.isPending}
                onClick={() => remove.mutate(removing)}
              >
                {remove.isPending ? "正在移除…" : "移除"}
              </button>
            </footer>
          </div>
        </Modal>
      )}
    </>
  );
}

// describe says when the screen last asked and what it reported.
function describe(screen: EinkScreen): string {
  if (!screen.seenAt) return "还没来过";
  const parts = [`${relativeTime(screen.seenAt)}来过`];
  const report = screen.report;
  if (report?.battery !== undefined) parts.push(`电量 ${report.battery}%${report.charging ? "（充电中）" : ""}`);
  if (report?.firmware) parts.push(`固件 ${report.firmware}`);
  if (report?.fail) parts.push(`上次失败：${report.fail}`);
  if (report?.history) parts.push(`最近醒来：${report.history}`);
  return parts.join(" · ");
}

function overdue(screen: EinkScreen, now = Date.now()): boolean {
  return !!screen.nextAt && Date.parse(screen.nextAt) + overdueAfterMs < now;
}

function PairDialog({ onClose, onPaired }: { onClose: () => void; onPaired: () => void }) {
  const [name, setName] = useState("");
  const [paired, setPaired] = useState<EinkPairedScreen>();
  const pair = useMutation({
    mutationFn: () => pairScreen(name.trim()),
    onSuccess: (result) => {
      setPaired(result);
      onPaired();
    },
  });
  if (paired) {
    return (
      <Modal title={`“${paired.screen.name}”已添加`} onClose={onClose}>
        <div className={styles.setDialogForm}>
          <p>把下面的令牌写进这块屏的配置。关掉这个窗口后再也看不到它；丢了就移除这块屏、重新添加。</p>
          <Field label="设备令牌">
            <input
              className={styles.setInput}
              readOnly
              value={paired.token}
              onFocus={(event) => event.target.select()}
            />
          </Field>
          <footer>
            <button type="button" className={`${styles.setBtn} primary`} onClick={onClose}>
              已经写好了
            </button>
          </footer>
        </div>
      </Modal>
    );
  }
  return (
    <Modal title="添加墨水屏" busy={pair.isPending} onClose={onClose}>
      <form
        className={styles.setDialogForm}
        onSubmit={(event) => {
          event.preventDefault();
          pair.mutate();
        }}
      >
        <Field label="名字" hint="放在哪里，例如“客厅”，1–6 个字">
          <input
            className={styles.setInput}
            value={name}
            maxLength={6}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        {pair.isError && <Banner tone="error">{errorText(pair.error, "eink")}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} disabled={pair.isPending} onClick={onClose}>
            取消
          </button>
          <button type="submit" className={`${styles.setBtn} primary`} disabled={pair.isPending || !name.trim()}>
            {pair.isPending ? "正在添加…" : "添加"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}

function RenameDialog({
  screen,
  onClose,
  onRenamed,
}: {
  screen: EinkScreen;
  onClose: () => void;
  onRenamed: () => void;
}) {
  const [name, setName] = useState(screen.name);
  const rename = useMutation({
    mutationFn: () => renameScreen(screen.id, name.trim()),
    onSuccess: () => {
      onRenamed();
      onClose();
    },
  });
  return (
    <Modal title={`给“${screen.name}”改名`} busy={rename.isPending} onClose={onClose}>
      <form
        className={styles.setDialogForm}
        onSubmit={(event) => {
          event.preventDefault();
          rename.mutate();
        }}
      >
        <Field label="名字" hint="1–6 个字">
          <input
            className={styles.setInput}
            value={name}
            maxLength={6}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        {rename.isError && <Banner tone="error">{errorText(rename.error, "eink")}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} disabled={rename.isPending} onClick={onClose}>
            取消
          </button>
          <button type="submit" className={`${styles.setBtn} primary`} disabled={rename.isPending || !name.trim()}>
            {rename.isPending ? "正在保存…" : "保存"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}
