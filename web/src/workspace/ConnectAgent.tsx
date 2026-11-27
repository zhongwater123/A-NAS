import { Check, Copy, LoaderCircle, TriangleAlert } from "lucide-react";
import { type FormEvent, type RefObject, useState } from "react";

import { errorText } from "../api/errorMessages";
import { copyText } from "../lib/clipboard";
import { namesDeviceByAddress } from "../lib/deviceHost";
import { Modal } from "../ui/modal";
import { defaultAgentName, promptFor } from "./agentPrompt";
import { useAgentTokens } from "./agentTokens";
import { type AgentKind, type AgentToken, createAgentToken } from "./api";
import { kindLabel } from "./words";
import styles from "./workspace.module.css";

type Created = { id: string; kind: AgentKind; prompt: string };

// ConnectAgent gives a member the prompt that has their Claude Code or Codex
// connect itself to the workspace, with a token made for that agent and shown
// only here, then waits for the agent's first call. Agents reach the device
// only at an address (namesDeviceByAddress), so a desktop opened by name
// makes no token.
export function ConnectAgent({
  account,
  container,
  onClose,
}: {
  account: string;
  container: RefObject<HTMLElement | null>;
  onClose: () => void;
}) {
  const [name, setName] = useState(defaultAgentName);
  const byAddress = namesDeviceByAddress(location.hostname);
  const [kind, setKind] = useState<AgentKind>("claude-code");
  const [created, setCreated] = useState<Created>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("给这个 agent 起个名字");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const result = await createAgentToken(trimmed, kind);
      const endpoint = `${location.protocol}//${location.host}/api/v1/workspace/mcp`;
      setCreated({ id: result.agentToken.id, kind, prompt: promptFor(kind, endpoint, result.token) });
    } catch (caught) {
      setError(errorText(caught, "workspace"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title="连接 agent" container={container} wide busy={busy} onClose={onClose}>
      {created ? (
        <Prompt account={account} created={created} onClose={onClose} />
      ) : (
        <form className={styles.wsForm} aria-label="连接 agent" onSubmit={(event) => void submit(event)}>
          <label className={styles.wsField}>
            <span>名字</span>
            <input
              className={styles.wsInput}
              value={name}
              maxLength={40}
              placeholder="办公电脑"
              autoFocus
              onChange={(event) => setName(event.target.value)}
            />
          </label>
          <fieldset className={styles.wsField}>
            <legend>agent</legend>
            <div className={styles.wsSegments}>
              {(["claude-code", "codex"] as const).map((value) => (
                <label key={value} className={styles.wsSegment}>
                  <input
                    type="radio"
                    name="kind"
                    value={value}
                    checked={kind === value}
                    onChange={() => setKind(value)}
                  />
                  {kindLabel[value]}
                </label>
              ))}
            </div>
          </fieldset>
          {!byAddress && (
            <p className={`${styles.wsNotice} ${styles.warn}`}>
              <TriangleAlert size={14} aria-hidden="true" />
              agent 只能按 IP 地址连接 NAS：用 NAS 的 IP 地址打开桌面，再生成提示词。
            </p>
          )}
          {error && (
            <p className={styles.wsError} role="alert">
              {error}
            </p>
          )}
          <footer className={styles.wsDialogFoot}>
            <button type="button" className={styles.wsButton} onClick={onClose}>
              取消
            </button>
            <button type="submit" className={`${styles.wsButton} ${styles.primary}`} disabled={busy || !byAddress}>
              {busy ? "正在生成…" : "生成提示词"}
            </button>
          </footer>
        </form>
      )}
    </Modal>
  );
}

function Prompt({ account, created, onClose }: { account: string; created: Created; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  // The agent's first call marks the token used.
  const used = (tokens?: AgentToken[]) => tokens?.some((token) => token.id === created.id && token.lastUsedAt) ?? false;
  const connected = used(useAgentTokens(account, (tokens) => (used(tokens) ? false : 2_000)).data);
  return (
    <div className={styles.wsForm}>
      <p className={styles.wsStep}>把这段话发给你电脑上的 {kindLabel[created.kind]}：</p>
      <div className={`${styles.wsCommand} ${styles.prompt}`}>
        <pre aria-label="提示词">{created.prompt}</pre>
        <button
          type="button"
          className={styles.wsButton}
          onClick={() => {
            setCopied(copyText(created.prompt));
          }}
        >
          {copied ? <Check size={15} aria-hidden="true" /> : <Copy size={15} aria-hidden="true" />}
          {copied ? "已复制" : "复制"}
        </button>
      </div>
      <p className={styles.wsFaint}>里面的令牌只显示这一次，只能用于共享工作区。</p>
      <p className={`${styles.wsWaiting} ${connected ? styles.done : ""}`} role="status">
        {connected ? (
          <>
            <Check size={15} aria-hidden="true" />
            已连接
          </>
        ) : (
          <>
            <LoaderCircle size={15} className="spinning" aria-hidden="true" />
            等待 agent 第一次调用
          </>
        )}
      </p>
      <footer className={styles.wsDialogFoot}>
        <button type="button" className={`${styles.wsButton} ${styles.primary}`} onClick={onClose}>
          完成
        </button>
      </footer>
    </div>
  );
}
