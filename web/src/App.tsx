import { Suspense, useEffect, useState } from "react";
import { ErrorBoundary } from "react-error-boundary";

import { onSessionEnded, setSession } from "./api/client";
import type { Session } from "./api.generated";
import { Authentication, AuthMode } from "./auth/Authentication";
import { currentSession, getSetupStatus, logout } from "./auth/api";
import { PasswordChange } from "./auth/PasswordChange";
import { Desktop } from "./desktop/Desktop";
import { isLocalConsole } from "./lib/localConsole";
import { transferStore } from "./transfers/store";
import { lazyCode } from "./ui/lazyCode";
import "./styles/index.css";
import "./desktop/desktop.module.css";

// Only the device's own screen has a screen saver, so only it downloads one.
const LocalConsoleScreenSaver = lazyCode(() =>
  import("./desktop/LocalConsoleScreenSaver").then((module) => module.LocalConsoleScreenSaver),
);

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
      {/* The sign-in pages have no screen saver but still go black. Without
          its code the screen stays lit; the page works on. */}
      {isLocalConsole() && (
        <ErrorBoundary
          fallback={null}
          onError={(error, info) => console.error("A-NAS screen saver failed", error, info.componentStack)}
        >
          <Suspense fallback={null}>
            <LocalConsoleScreenSaver enabled videos={signedIn} />
          </Suspense>
        </ErrorBoundary>
      )}
    </>
  );
}
