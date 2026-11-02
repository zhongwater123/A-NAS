import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";

import "../styles/tokens.css";
import { CompanionDesktop } from "./CompanionDesktop";
import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import styles from "./companion.module.css";
import { useCompanion } from "./store";
import type { TaskPhase } from "./taskState";

function Playground() {
  const [, update] = useCompanion("local-playground");
  const [phase, setPhase] = useState<TaskPhase>("idle");
  if (new URLSearchParams(window.location.search).get("desktop") === "1") {
    return <CompanionDesktop userId="local-playground" />;
  }
  return (
    <div className={styles.lab}>
      <nav className={styles.labNav}>
        <strong>A-NAS</strong>
        <span>伙伴交互实验室</span>
        <small>本机离线试玩</small>
      </nav>
      <div className={styles.labContent}>
        <CompanionPanel userId="local-playground" previewPhase={phase} />
        <details className={styles.rehearsal}>
          <summary>任务与情绪联动演示（模拟）</summary>
          <p>只预演动作与情绪，不运行 AI 或保存文件。连接 NAS 后由真实 VNAS 任务状态驱动。</p>
          <div className={styles.actions}>
            {(
              [
                ["thinking", "开始思考"],
                ["working", "执行中"],
                ["waiting", "等我确认"],
                ["success", "回答完成"],
                ["error", "遇到困难"],
                ["idle", "回到日常"],
              ] as [TaskPhase, string][]
            ).map(([value, label]) => (
              <button type="button" key={value} aria-pressed={phase === value} onClick={() => setPhase(value)}>
                {label}
              </button>
            ))}
          </div>
        </details>
      </div>
      <CompanionFloating userId="local-playground" previewPhase={phase} onOpen={() => update({ place: "home" })} />
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Playground />
  </StrictMode>,
);
