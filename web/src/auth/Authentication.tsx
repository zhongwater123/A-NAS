import { LockKeyhole, UserRound } from "lucide-react";
import { FormEvent, useState } from "react";

import { errorText } from "../api/errorMessages";
import type { Session } from "../api.generated";
import { isLocalConsole } from "../desktop/LocalConsoleScreenSaver";
import { AuthError, AuthField, AuthHero, AuthStage, AuthSubmit } from "./AuthStage";
import { passwordProblem, passwordRuleText, usernameProblem, usernameRuleText } from "./accountRules";
import { login, setupAdministrator } from "./api";
import styles from "./auth.module.css";

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
  if (mode === "loading")
    return (
      <AuthStage>
        <div className={styles.authPanel}>
          <header className={styles.authHero}>
            <p className={styles.authEyebrow}>PLEASE STAND BY</p>
            <h1 className={styles.authStatus}>
              正在启动 A-NAS…
              <span className={styles.authCursor} aria-hidden="true" />
            </h1>
          </header>
        </div>
      </AuthStage>
    );
  // A LAN browser may not claim a device that is not set up yet.
  if (mode === "setup-elsewhere")
    return (
      <AuthStage>
        <section className={styles.authPanel} aria-label="在本机启用 A-NAS">
          <AuthHero eyebrow="PLEASE STAND BY" title="请在 NAS 本机启用" />
          <div className={styles.authFields}>
            <div className={styles.authNote}>
              <p>这台 A-NAS 还没有管理员。为防止局域网中的其他人抢先认领，首个管理员只能在 NAS 自己的屏幕上创建。</p>
              <p>
                没有接屏幕时，用 SSH 隧道把 NAS 的 127.0.0.1:8080 转到这台电脑，再打开 http://127.0.0.1:8080 完成启用。
              </p>
            </div>
            <AuthSubmit label="已启用，刷新页面" onClick={() => window.location.reload()} />
          </div>
        </section>
      </AuthStage>
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
  const setup = mode === "setup";
  return (
    <AuthStage>
      <form className={styles.authPanel} autoComplete="off" onSubmit={(event) => void submit(event)}>
        <AuthHero
          eyebrow={setup ? "FIRST SIGN-ON" : "SIGN ON"}
          title={setup ? "启用 A-NAS" : "欢迎回来"}
          lede={setup ? "创建本机管理员账号以启用设备" : "登录以继续使用你的家庭存储"}
        />
        <div className={styles.authFields}>
          <AuthField
            label="账号"
            icon={<UserRound />}
            name="username"
            required
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            hint={setup ? usernameRuleText : undefined}
          />
          <AuthField
            label="密码"
            icon={<LockKeyhole />}
            name="password"
            type="password"
            minLength={12}
            required
            autoComplete={setup ? "new-password" : "current-password"}
            hint={setup ? passwordRuleText : undefined}
          />
          {error && <AuthError text={error} />}
          <AuthSubmit label={setup ? "创建管理员并启用" : "登录"} submitting={submitting} />
        </div>
      </form>
    </AuthStage>
  );
}
