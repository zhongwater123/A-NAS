import { KeyRound, Trash2 } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";

import { errorText } from "../api/errorMessages";
import { type AssistantKeyState, deleteAssistantKey, readAssistantKey, saveAssistantKey } from "../assistant/key";
import { PasswordInput } from "./AccountSection";
import { Banner, Field, Group, IconTile, Modal, PageHeader, Pill, Row, type Tone } from "./parts";
import styles from "./settings.module.css";

// What goes to DeepSeek while a key is saved: the administrator reads it here and confirms it
// before saving (docs/decisions/2026-11-17-assistant-v1-cloud-model.md, decision 6).
const disclosure =
  "成员与 VNAS 的对话、VNAS 为回答读取的文件内容和照片信息、它查看的照片和桌面画面，都会发到 DeepSeek 的服务器处理。";

const gatewayText: Record<AssistantKeyState["gateway"], { text: string; tone: Tone } | undefined> = {
  running: undefined,
  starting: { text: "模型网关启动中", tone: "accent" },
  stopped: { text: "模型网关未运行", tone: "warning" },
  failed: { text: "模型网关出错", tone: "danger" },
};

// AssistantSection is Settings → VNAS, shown to administrators only: the household's DeepSeek
// key, which gives every member VNAS. Once saved it is only replaced or deleted, never shown.
export function AssistantSection({ onKeyChange }: { onKeyChange?: () => void }) {
  const [state, setState] = useState<AssistantKeyState | null>(null);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const [dialog, setDialog] = useState<"save" | "delete" | null>(null);
  useEffect(() => {
    let active = true;
    readAssistantKey()
      .then((next) => active && setState(next))
      .catch((caught: unknown) => active && setError(errorText(caught, "assistantKey")));
    return () => {
      active = false;
    };
  }, []);
  const changed = (next: AssistantKeyState, message: string) => {
    setState(next);
    setDialog(null);
    setError("");
    setSaved(message);
    onKeyChange?.();
  };
  const gateway = state?.configured ? gatewayText[state.gateway] : undefined;
  return (
    <>
      <PageHeader title="VNAS" />
      {error && (
        <Banner tone="error" onClose={() => setError("")}>
          {error}
        </Banner>
      )}
      {saved && (
        <Banner tone="success" onClose={() => setSaved("")}>
          {saved}
        </Banner>
      )}
      <Group title="DeepSeek 密钥" anchor="assistant-key">
        <Row
          label={
            <>
              API 密钥
              {state && (
                <Pill tone={state.configured ? "success" : "neutral"}>{state.configured ? "已填写" : "未填写"}</Pill>
              )}
              {gateway && <Pill tone={gateway.tone}>{gateway.text}</Pill>}
            </>
          }
          detail={state?.configured ? "全家成员都能使用 VNAS" : "填写后，全家成员都能使用 VNAS"}
        >
          {state?.configured && (
            <button type="button" className={`${styles.setBtn} ${styles.danger}`} onClick={() => setDialog("delete")}>
              删除
            </button>
          )}
          <button
            type="button"
            className={`${styles.setBtn} ${state?.configured ? "" : "primary"}`}
            disabled={!state}
            onClick={() => setDialog("save")}
          >
            {state?.configured ? "替换" : "填写"}
          </button>
        </Row>
        <Row label="发往 DeepSeek 的内容" detail={disclosure} anchor="assistant-cloud" />
      </Group>
      {dialog === "save" && (
        <SaveDialog
          replacing={state?.configured ?? false}
          onClose={() => setDialog(null)}
          onSaved={(next) => changed(next, "已保存，全家成员都能使用 VNAS")}
        />
      )}
      {dialog === "delete" && (
        <DeleteDialog onClose={() => setDialog(null)} onDeleted={(next) => changed(next, "已删除，VNAS 已关闭")} />
      )}
    </>
  );
}

function SaveDialog({
  replacing,
  onClose,
  onSaved,
}: {
  replacing: boolean;
  onClose: () => void;
  onSaved: (state: AssistantKeyState) => void;
}) {
  const [agreed, setAgreed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const key = String(new FormData(event.currentTarget).get("key") ?? "").trim();
    setBusy(true);
    setError("");
    try {
      onSaved(await saveAssistantKey(key, agreed));
    } catch (caught) {
      setError(errorText(caught, "assistantKey"));
      setBusy(false);
    }
  };
  const title = replacing ? "替换 DeepSeek 密钥" : "填写 DeepSeek 密钥";
  return (
    <Modal title={title} icon={<IconTile tone="accent" icon={KeyRound} size="lg" />} busy={busy} onClose={onClose}>
      <form className={styles.setDialogForm} aria-label={title} autoComplete="off" onSubmit={submit}>
        <Field label="API 密钥" hint="在 DeepSeek 开放平台创建。保存后不再显示，只能替换或删除">
          <PasswordInput name="key" autoComplete="off" minLength={16} autoFocus />
        </Field>
        <p className={styles.setDisclosure}>{disclosure}</p>
        <label className={styles.setConsent}>
          <input type="checkbox" checked={agreed} onChange={(event) => setAgreed(event.target.checked)} />
          <span>我已了解，并同意这些内容发往 DeepSeek</span>
        </label>
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className={`${styles.setBtn} primary`} disabled={busy || !agreed}>
            {busy ? "正在保存…" : "保存"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}

function DeleteDialog({ onClose, onDeleted }: { onClose: () => void; onDeleted: (state: AssistantKeyState) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const remove = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDeleted(await deleteAssistantKey());
    } catch (caught) {
      setError(errorText(caught, "assistantKey"));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="删除 DeepSeek 密钥"
      description="删除后全家成员都不能使用 VNAS。对话、记忆和定时任务会保留，重新填写密钥后恢复。"
      icon={<IconTile tone="danger" icon={Trash2} size="lg" />}
      busy={busy}
      onClose={onClose}
    >
      <form className={styles.setDialogForm} aria-label="删除 DeepSeek 密钥" onSubmit={remove}>
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className={`${styles.setBtn} ${styles.danger}`} disabled={busy}>
            {busy ? "正在删除…" : "删除"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}
