import { LockKeyhole } from "lucide-react";
import { FormEvent, useState } from "react";

import { errorText } from "../api/errorMessages";
import { AuthError, AuthField, AuthHero, AuthStage, AuthSubmit } from "./AuthStage";
import { passwordProblem, passwordRuleText } from "./accountRules";
import { changePassword } from "./api";
import styles from "./auth.module.css";

// PasswordChange blocks the desktop after an administrator reset the
// password, so only the member knows the password in use.
export function PasswordChange({ onChanged, onLogout }: { onChanged: () => void; onLogout: () => void }) {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
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
    <AuthStage>
      <form className={styles.authPanel} autoComplete="off" onSubmit={(event) => void submit(event)}>
        <AuthHero
          eyebrow="PASSWORD RESET"
          title="设置新密码"
          lede="管理员重置了你的密码。继续使用 A-NAS 和 SMB 共享前，请设置只有你知道的新密码。"
        />
        <div className={styles.authFields}>
          <AuthField
            label="当前密码"
            icon={<LockKeyhole />}
            name="currentPassword"
            type="password"
            required
            autoComplete="current-password"
          />
          <AuthField
            label="新密码"
            icon={<LockKeyhole />}
            name="newPassword"
            type="password"
            minLength={12}
            required
            autoComplete="new-password"
            hint={passwordRuleText}
          />
          <AuthField
            label="确认新密码"
            icon={<LockKeyhole />}
            name="confirmPassword"
            type="password"
            minLength={12}
            required
            autoComplete="new-password"
          />
          {error && <AuthError text={error} />}
          <AuthSubmit label="保存新密码" submitting={submitting} />
          <button type="button" className={styles.authLink} onClick={onLogout}>
            退出登录
          </button>
        </div>
      </form>
    </AuthStage>
  );
}
