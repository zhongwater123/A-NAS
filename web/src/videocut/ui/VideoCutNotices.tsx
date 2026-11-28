// 剪辑中心 tells a member on the desktop how their films go
// (docs/specs/videocut-center.md, 桌面提示): a job waiting for VNAS to plan it
// and why, a round of VNAS planning it, a job needing them, a film just
// saved into the photo library, or a job that failed and why. The desktop
// only shows these; planning runs on the device whether it is open or not.
import { Clapperboard } from "lucide-react";

import { DeskNotice } from "../../ui/DeskNotice";
import { useNotices, type VideoCutNotice } from "../app/notices";
import { forMember } from "../lib/words";
import styles from "./notices.module.css";

export function VideoCutNotices({ onOpen }: { onOpen: () => void }) {
  const { notices, dismiss } = useNotices();
  return (
    <DeskNotice
      className={styles.mnNotice}
      label="剪辑中心提醒"
      source="剪辑中心"
      icon={<Clapperboard />}
      lines={summarize(notices)}
      onOpen={onOpen}
      onDismiss={dismiss}
    />
  );
}

// summarize says each notice in a line, the projects named; a job that still
// waits for VNAS says why, when VideoCut gave a reason.
export function summarize(notices: VideoCutNotice[]): string[] {
  return notices.map((notice) => {
    const project = `「${notice.project}」`;
    switch (notice.kind) {
      case "waiting_for_planning":
        return `${project}在等 VNAS 规划${notice.detail ? `：${notice.detail}` : ""}`;
      case "planning":
        return `${project}正在由 VNAS 规划`;
      case "needs_input":
        return `${project}需要你处理一下`;
      case "saved":
        return `${project}的成片已存进相册`;
      case "failed": {
        const said = forMember(notice.detail);
        return `${project}没有剪成${said ? `：${said}` : ""}`;
      }
      default:
        return project;
    }
  });
}
