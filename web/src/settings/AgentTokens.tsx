import { useEffect, useState } from "react";

import { relativeTime } from "../lib/time";
import { type AgentToken, listAgentTokens, revokeAgentToken } from "../workspace/api";
import { kindLabel } from "../workspace/words";
import { Group, Modal, messageOf, Row, useSettingsKit } from "./parts";
import styles from "./settings.module.css";

// AgentTokens is 我的账号 → 已连接的 agent: the agents this account connected
// to 共享工作区, each with its own token (ADR 0040), and the way to cut one
// off. An account that connected none sees nothing here.
export function AgentTokens() {
  const { toast } = useSettingsKit();
  const [tokens, setTokens] = useState<AgentToken[]>();
  const [revoking, setRevoking] = useState<AgentToken>();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    listAgentTokens(controller.signal).then(setTokens, () => undefined);
    return () => controller.abort();
  }, []);
  if (!tokens?.length) return null;
  const revoke = async (token: AgentToken) => {
    setBusy(true);
    try {
      await revokeAgentToken(token.id);
      setTokens((current) => current?.filter((item) => item.id !== token.id));
      setRevoking(undefined);
      toast({ text: `已撤销“${token.name}”，它之后的调用都会被拒绝。` });
    } catch (error) {
      toast({ text: messageOf(error), tone: "error" });
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Group title="已连接的 agent" anchor="agent-tokens">
        {tokens.map((token) => (
          <Row
            key={token.id}
            label={token.name}
            detail={`${kindLabel[token.kind]} · ${token.lastUsedAt ? `${relativeTime(token.lastUsedAt)}使用` : "还没用过"}`}
          >
            <button type="button" className={styles.setBtn} onClick={() => setRevoking(token)}>
              撤销
            </button>
          </Row>
        ))}
      </Group>
      {revoking && (
        <Modal title={`撤销“${revoking.name}”？`} busy={busy} onClose={() => setRevoking(undefined)}>
          <div className={styles.setDialogForm}>
            <p>这个 agent 之后读写共享工作区都会被拒绝，要再用就重新连接。</p>
            <footer>
              <button type="button" className={styles.setBtn} disabled={busy} onClick={() => setRevoking(undefined)}>
                取消
              </button>
              <button
                type="button"
                className={`${styles.setBtn} primary ${styles.danger}`}
                disabled={busy}
                onClick={() => void revoke(revoking)}
              >
                {busy ? "正在撤销…" : "撤销"}
              </button>
            </footer>
          </div>
        </Modal>
      )}
    </>
  );
}
