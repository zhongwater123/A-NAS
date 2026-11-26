import { useQuery } from "@tanstack/react-query";
import { FileX2, LogIn, RotateCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { onSessionEnded, setSession } from "../api/client";
import type { Session } from "../api.generated";
import { currentSession } from "../auth/api";
import { LocalConsoleScreenSaver } from "../desktop/LocalConsoleScreenSaver";
import { isLocalConsole } from "../lib/localConsole";
import { loadPeople } from "../people/people";
import { getPage } from "./api";
import { PageView, pageKey } from "./PageView";
import { WorkspaceQueries } from "./queries";
import styles from "./workspace.module.css";

const tabClass = styles.wsApp;

// PageTab is one shared page on its own in a browser tab (workspace.html,
// served at /workspace/{pageId}): the page fills the tab, in the same
// sandboxed frame as ever (ADR 0040), under a thin bar. The tab signs in with
// the desktop's session. On the device's own screen a tab has no browser
// bar, so 关闭 takes the member back to the desktop, and the tab is what the
// screen shows, so it keeps the desktop's screen saver and 息屏. The screen
// saver is imported, not loaded on demand: the desktop imports it too, so
// both share its chunk and the desktop's first load keeps one stylesheet.
export function PageTab({ pageId }: { pageId: string }) {
  // undefined while it asks, null when nobody is signed in.
  const [session, setSessionState] = useState<Session | null>();
  return (
    <>
      <SignedIn pageId={pageId} session={session} onSession={setSessionState} />
      {/* The sign-in message has no screen saver but still goes black, as on the desktop. */}
      <LocalConsoleScreenSaver enabled={isLocalConsole()} videos={Boolean(session)} />
    </>
  );
}

function SignedIn({
  pageId,
  session,
  onSession: setSessionState,
}: {
  pageId: string;
  session: Session | null | undefined;
  onSession: (session: Session | null) => void;
}) {
  useEffect(() => {
    let active = true;
    currentSession().then(
      (value) => active && setSessionState(value),
      () => active && setSessionState(null),
    );
    return () => {
      active = false;
    };
  }, [setSessionState]);
  useEffect(() => {
    if (!session) return;
    // Names and faces, as the desktop shows them.
    void loadPeople();
    return onSessionEnded(() => {
      setSession();
      setSessionState(null);
    });
  }, [session, setSessionState]);

  if (session === undefined) {
    return (
      <div className={tabClass}>
        <div className={styles.wsCentered}>
          <span className="loader" role="status" aria-label="正在打开页面" />
        </div>
      </div>
    );
  }
  if (session === null) {
    return (
      <div className={tabClass}>
        <div className={styles.wsCentered}>
          <LogIn className={styles.wsCenteredIcon} aria-hidden="true" />
          <p>请先登录 A-NAS 桌面</p>
          <div className={styles.wsActions}>
            <a className={`${styles.wsButton} ${styles.primary}`} href="/" target="_blank" rel="noopener">
              打开桌面
            </a>
            <button type="button" className={styles.wsButton} onClick={() => window.location.reload()}>
              <RotateCw size={15} aria-hidden="true" />
              重新载入
            </button>
          </div>
        </div>
      </div>
    );
  }
  return (
    <WorkspaceQueries className={tabClass}>
      <Tab pageId={pageId} userId={session.user.id} />
    </WorkspaceQueries>
  );
}

function Tab({ pageId, userId }: { pageId: string; userId: string }) {
  const [gone, setGone] = useState(false);
  const onGone = useCallback(() => setGone(true), []);
  // PageView reads the page; this only follows its title.
  const title = useQuery({
    queryKey: pageKey(pageId),
    queryFn: ({ signal }) => getPage(pageId, signal),
    enabled: false,
  }).data?.title;
  useEffect(() => {
    document.title = title ? `${title} · 共享工作区` : "共享工作区";
  }, [title]);
  const close = isLocalConsole() ? () => window.close() : undefined;
  return (
    <div className={tabClass}>
      {gone ? (
        <div className={styles.wsCentered}>
          <FileX2 className={styles.wsCenteredIcon} aria-hidden="true" />
          <p>这个页面已被删除</p>
          {close && (
            <button type="button" className={styles.wsButton} onClick={close}>
              关闭
            </button>
          )}
        </div>
      ) : (
        <PageView pageId={pageId} userId={userId} onGone={onGone} onClose={close} />
      )}
    </div>
  );
}
