import { FormEvent, useId, useState } from "react";

import { errorText } from "../api/errorMessages";
import type { Session } from "../api.generated";
import styles from "../desktop/desktop.module.css";
import { isLocalConsole } from "../desktop/LocalConsoleScreenSaver";
import { passwordProblem, passwordRuleText, usernameProblem, usernameRuleText } from "./accountRules";
import { login, setupAdministrator } from "./api";

export type AuthMode = "loading" | "setup" | "setup-elsewhere" | "login";

export function Authentication({
  mode,
  error: initialError,
  onAuthenticated,
}: {
  mode: AuthMode;
  error: string;
  onAuthenticated: (session: Session) => void;
}) {
  const [error, setError] = useState(initialError);
  const [submitting, setSubmitting] = useState(false);
  const usernameHint = useId();
  const passwordHint = useId();
  if (mode === "loading")
    return (
      <main className={styles.authShell}>
        <div className={styles.authCard}>
          <span className="loader" />
          <h1>正在启动 A-NAS…</h1>
        </div>
      </main>
    );
  // A LAN browser may not claim a device that is not set up yet.
  if (mode === "setup-elsewhere")
    return (
      <main className={styles.authShell}>
        <section className={styles.authCard} aria-label="在本机启用 A-NAS">
          <span className={`${styles.brandMark} ${styles.large}`}>A</span>
          <p className={styles.sectionLabel}>A-NAS v1.0.1 PREVIEW</p>
          <h1>请在 NAS 本机启用</h1>
          <p>这台 A-NAS 还没有管理员。为防止局域网中的其他人抢先认领，首个管理员只能在 NAS 自己的屏幕上创建。</p>
          <p>没有接屏幕时，用 SSH 隧道把 NAS 的 127.0.0.1:8080 转到这台电脑，再打开 http://127.0.0.1:8080 完成启用。</p>
          <button className="primary" onClick={() => window.location.reload()}>
            已启用，刷新页面
          </button>
        </section>
      </main>
    );
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    const data = new FormData(event.currentTarget);
    // Signing in applies no rules, so passwords set under earlier ones keep working.
    const problem =
      mode === "setup"
        ? (usernameProblem(String(data.get("username"))) ?? passwordProblem(String(data.get("password"))))
        : undefined;
    if (problem) {
      setError(problem);
      return;
    }
    setSubmitting(true);
    try {
      const session =
        mode === "setup"
          ? await setupAdministrator(String(data.get("username")), String(data.get("password")), isLocalConsole())
          : await login(String(data.get("username")), String(data.get("password")), isLocalConsole());
      onAuthenticated(session);
    } catch (caught) {
      setError(errorText(caught));
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <main className={styles.authShell}>
      <form className={styles.authCard} autoComplete="off" onSubmit={(event) => void submit(event)}>
        <span className={`${styles.brandMark} ${styles.large}`}>A</span>
        <p className={styles.sectionLabel}>A-NAS v1.0.1 PREVIEW</p>
        <h1>{mode === "setup" ? "启用 A-NAS" : "登录 A-NAS"}</h1>
        {mode === "setup" && <p>创建本机管理员账号以启用设备。</p>}
        <div className={styles.authField}>
          <label>
            账号
            <input
              name="username"
              required
              autoComplete="username"
              autoCapitalize="none"
              spellCheck={false}
              aria-describedby={mode === "setup" ? usernameHint : undefined}
            />
          </label>
          {mode === "setup" && <small id={usernameHint}>{usernameRuleText}</small>}
        </div>
        <div className={styles.authField}>
          <label>
            密码
            <input
              name="password"
              type="password"
              minLength={12}
              required
              autoComplete={mode === "setup" ? "new-password" : "current-password"}
              aria-describedby={mode === "setup" ? passwordHint : undefined}
            />
          </label>
          {mode === "setup" && <small id={passwordHint}>{passwordRuleText}</small>}
        </div>
        {error && <p className={styles.formError}>{error}</p>}
        <button className="primary" disabled={submitting}>
          {submitting ? "处理中…" : mode === "setup" ? "创建管理员并启用" : "登录"}
        </button>
      </form>
    </main>
  );
}
