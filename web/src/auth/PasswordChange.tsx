import { FormEvent, useId, useState } from "react";

import { errorText } from "../api/errorMessages";
import { passwordProblem, passwordRuleText } from "./accountRules";
import { changePassword } from "./api";

// PasswordChange blocks the desktop after an administrator reset the
// password, so only the member knows the password in use.
export function PasswordChange({ onChanged, onLogout }: { onChanged: () => void; onLogout: () => void }) {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const hint = useId();
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    const data = new FormData(event.currentTarget);
    const next = String(data.get("newPassword"));
    const problem = passwordProblem(next);
    if (problem) {
      setError(problem);
      return;
    }
    if (next !== String(data.get("confirmPassword"))) {
      setError("两次输入的新密码不一致");
      return;
    }
    setSubmitting(true);
    try {
      await changePassword(String(data.get("currentPassword")), next);
      onChanged();
    } catch (caught) {
      setError(errorText(caught));
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <main className="auth-shell">
      <form className="auth-card" autoComplete="off" onSubmit={(event) => void submit(event)}>
        <span className="brand-mark large">A</span>
        <p className="section-label">PASSWORD RESET</p>
        <h1>设置新密码</h1>
        <p>管理员重置了你的密码。继续使用 A-NAS 和 SMB 共享前，请设置只有你知道的新密码。</p>
        <label>
          当前密码
          <input name="currentPassword" type="password" required autoComplete="current-password" />
        </label>
        <div className="auth-field">
          <label>
            新密码
            <input
              name="newPassword"
              type="password"
              minLength={12}
              required
              autoComplete="new-password"
              aria-describedby={hint}
            />
          </label>
          <small id={hint}>{passwordRuleText}</small>
        </div>
        <label>
          确认新密码
          <input name="confirmPassword" type="password" minLength={12} required autoComplete="new-password" />
        </label>
        {error && <p className="form-error">{error}</p>}
        <button className="primary" disabled={submitting}>
          {submitting ? "处理中…" : "保存新密码"}
        </button>
        <button type="button" className="link-button" onClick={onLogout}>
          退出登录
        </button>
      </form>
    </main>
  );
}
