import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "../styles/tokens.css";
import { CompanionDesktop } from "./CompanionDesktop";
import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import styles from "./companion.module.css";
import { useCompanion } from "./store";

function Playground() {
  const [, update] = useCompanion("local-playground");
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
        <CompanionPanel userId="local-playground" />
      </div>
      <CompanionFloating userId="local-playground" onOpen={() => update({ place: "home" })} />
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Playground />
  </StrictMode>,
);
