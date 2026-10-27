import { useEffect, useState } from "react";

import type { Session } from "./api.generated";
import { onSessionEnded, setSession } from "./api/client";
import { currentSession, getSetupStatus, logout } from "./auth/api";
import { AuthMode, Authentication } from "./auth/Authentication";
import { PasswordChange } from "./auth/PasswordChange";
import { Desktop } from "./desktop/Desktop";
import { isLocalConsole, LocalConsoleScreenSaver } from "./desktop/LocalConsoleScreenSaver";
import { transferStore } from "./transfers/store";
import "./styles.css";

export default function App() {
  const [session, setSessionState] = useState<Session>();
  const [mode, setMode] = useState<AuthMode>("loading");
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void getSetupStatus()
      .then(async ({ setupRequired, setupAllowed }) => {
        if (!active) return;
        if (setupRequired) setMode(setupAllowed === false ? "setup-elsewhere" : "setup");
        else {
          try {
            const value = await currentSession();
            if (active) setSessionState(value);
          } catch {
            if (active) setMode("login");
          }
        }
      })
      .catch(() => {
        if (active) {
          setError("无法连接 A-NAS 产品服务");
          setMode("login");
        }
      });
    return () => {
      active = false;
    };
  }, []);
  // Browser sessions last 12 hours and any session can be revoked; once
  // polls are rejected, sign in again rather than report a lost connection.
  useEffect(() => {
    if (!session) return;
    return onSessionEnded(() => {
      setSession();
      setSessionState(undefined);
      setError("登录已过期，请重新登录");
      setMode("login");
    });
  }, [session]);
  const authenticated = (value: Session) => {
    setError("");
    setSessionState(value);
  };
  const signOut = () => {
    transferStore.reset();
    void logout().finally(() => {
      setSessionState(undefined);
      setMode("login");
    });
  };
  const signedIn = Boolean(session && !session.user.mustChangePassword);
  return (
    <>
      {!session ? (
        <Authentication mode={mode} error={error} onAuthenticated={authenticated} />
      ) : session.user.mustChangePassword ? (
        <PasswordChange
          onChanged={() => setSessionState({ ...session, user: { ...session.user, mustChangePassword: false } })}
          onLogout={signOut}
        />
      ) : (
        <Desktop
          session={session}
          onLogout={signOut}
          onUserChange={(user) => setSessionState((current) => current && { ...current, user })}
        />
      )}
      {/* The sign-in pages have no screen saver but still go black. */}
      <LocalConsoleScreenSaver enabled={isLocalConsole()} videos={signedIn} />
    </>
  );
}
