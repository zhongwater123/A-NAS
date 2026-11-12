import { Camera, Eye, EyeOff, TriangleAlert } from "lucide-react";
import { type FormEvent, useEffect, useEffectEvent, useState } from "react";

import type { Session, User } from "../api.generated";
import { nicknameProblem, nicknameRuleText, passwordProblem, passwordRuleText } from "../auth/accountRules";
import { changePassword, removeAvatar, updateNickname } from "../auth/api";
import { isLocalConsole } from "../desktop/LocalConsoleScreenSaver";
import { displayName, fullName, usePeople } from "../people/people";
import { AvatarDialog } from "./AvatarDialog";
import { roleLabel, statusLabel } from "./labels";
import { Avatar, Banner, Field, Group, Modal, messageOf, PageHeader, Row, useSettingsKit } from "./parts";
import styles from "./settings.module.css";

type Editing = "avatar" | "nickname" | "password";

// AccountSection is Settings → 我的账号: the profile everyone in the household
// sees, the password, and this sign-in. Each change happens in a dialog and
// reports its outcome in a toast.
export function AccountSection({
  session,
  openPassword,
  onPasswordShown,
  onUserChange,
  onLogout,
  onShowSignIns,
}: {
  session: Session;
  // 用户与权限 opens this section with the password dialog showing.
  openPassword: boolean;
  onPasswordShown: () => void;
  // onUserChange receives the account after its owner changed the nickname or avatar.
  onUserChange: (user: User) => void;
  onLogout: () => void;
  onShowSignIns: () => void;
}) {
  const { user } = session;
  const { toast } = useSettingsKit();
  const people = usePeople();
  const [editing, setEditing] = useState<Editing | undefined>(openPassword ? "password" : undefined);
  const shown = useEffectEvent(() => {
    if (openPassword) onPasswordShown();
  });
  useEffect(() => shown(), []);
  const [removing, setRemoving] = useState(false);
  const saved = (next: User, text: string) => {
    onUserChange(next);
    setEditing(undefined);
    toast({ text });
  };
  const dropAvatar = async () => {
    setRemoving(true);
    try {
      saved(await removeAvatar(), "已移除头像，现在显示文字头像。");
    } catch (caught) {
      toast({ text: messageOf(caught), tone: "error" });
    } finally {
      setRemoving(false);
    }
  };
  const reset = session.credentialReset;
  const resetBy = reset?.actorUsername ? people.byUsername(reset.actorUsername) : undefined;
  const localConsole = isLocalConsole();
  return (
    <>
      <PageHeader title="我的账号" />
      <div className={styles.acctHero}>
        <button
          className={styles.acctAvatar}
          aria-label="更换头像"
          title="更换头像"
          data-setting="avatar"
          onClick={() => setEditing("avatar")}
        >
          <Avatar who={user} size="xl" />
          <span className={styles.acctAvatarBadge} aria-hidden="true">
            <Camera />
          </span>
        </button>
        <h3>{displayName(user)}</h3>
        <p>
          {roleLabel(user.role)}
          {user.status !== "active" && (
            <span className={`${styles.acctState} ${styles[`status-${user.status}`] ?? ""}`}>
              {statusLabel(user.status)}
            </span>
          )}
        </p>
        {user.avatarVersion && (
          <button className={styles.setLink} disabled={removing} onClick={() => void dropAvatar()}>
            {removing ? "正在移除…" : "移除头像"}
          </button>
        )}
      </div>
      {/* The nickname and avatar are what other accounts see; sign-in and SMB
          use the account name. */}
      <Group title="个人资料">
        <Row label="昵称" value={user.nickname || "未设置"} anchor="nickname" onClick={() => setEditing("nickname")} />
        <Row label="账号名" value={<span className={styles.setMono}>{user.username}</span>} />
      </Group>
      <Group title="登录与安全">
        <Row label="密码" value="修改" anchor="password" onClick={() => setEditing("password")} />
        {reset && (
          <Row
            icon={<TriangleAlert className={styles.setRowWarn} aria-hidden="true" />}
            label="管理员重置过密码"
            detail={`${reset.actorUsername ? fullName(resetBy ?? { username: reset.actorUsername }) : "管理员"} 于 ${new Date(reset.at).toLocaleString("zh-CN")} 重置了这个账号的密码。此后的密码若不是你自己设置的，请当面确认。`}
          />
        )}
        <Row
          label="本次登录"
          value={localConsole ? "本机屏幕，退出前一直保持" : `浏览器，${formatDateTime(session.expiresAt)} 到期`}
        />
        <Row label="登录记录" anchor="sign-ins" onClick={onShowSignIns} />
      </Group>
      <Group>
        <button
          className={`${styles.setRow} ${styles.setRowCommand} ${styles.danger}`}
          data-setting="sign-out"
          onClick={onLogout}
        >
          退出登录
        </button>
      </Group>
      {editing === "avatar" && (
        <AvatarDialog onClose={() => setEditing(undefined)} onSaved={(next) => saved(next, "头像已更新。")} />
      )}
      {editing === "nickname" && (
        <NicknameDialog
          user={user}
          onClose={() => setEditing(undefined)}
          onSaved={(next) =>
            saved(
              next,
              next.nickname ? `昵称已改为“${next.nickname}”。` : `已清除昵称，现在显示账号名 ${next.username}。`,
            )
          }
        />
      )}
      {editing === "password" && (
        <PasswordDialog
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            toast({ text: "密码已更新，SMB 凭据已同步。" });
          }}
        />
      )}
    </>
  );
}

function NicknameDialog({
  user,
  onClose,
  onSaved,
}: {
  user: User;
  onClose: () => void;
  onSaved: (user: User) => void;
}) {
  const [value, setValue] = useState(user.nickname ?? "");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const problem = nicknameProblem(value);
    if (problem) {
      setError(problem);
      return;
    }
    setSaving(true);
    try {
      onSaved(await updateNickname(value.trim()));
    } catch (caught) {
      setError(messageOf(caught));
      setSaving(false);
    }
  };
  return (
    <Modal title="修改昵称" busy={saving} onClose={onClose}>
      <form
        className={styles.setDialogForm}
        aria-label="修改昵称"
        autoComplete="off"
        onSubmit={(event) => void submit(event)}
      >
        <Field label="昵称" hint={`${nicknameRuleText}。留空则显示账号名。`}>
          <input
            className={styles.setInput}
            name="nickname"
            value={value}
            onChange={(event) => setValue(event.target.value)}
            placeholder={user.username}
            autoFocus
            spellCheck={false}
          />
        </Field>
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} onClick={onClose} disabled={saving}>
            取消
          </button>
          <button className={`${styles.setBtn} primary`} disabled={saving}>
            {saving ? "正在保存…" : "保存昵称"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}

function PasswordDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
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
    setSaving(true);
    try {
      await changePassword(String(data.get("currentPassword")), next);
      onSaved();
    } catch (caught) {
      setError(messageOf(caught));
      setSaving(false);
    }
  };
  return (
    <Modal title="修改密码" description="网页登录和 SMB 共享会一起改用新密码。" busy={saving} onClose={onClose}>
      <form
        className={styles.setDialogForm}
        aria-label="修改密码"
        autoComplete="off"
        onSubmit={(event) => void submit(event)}
      >
        <Field label="当前密码">
          <PasswordInput name="currentPassword" autoComplete="current-password" autoFocus />
        </Field>
        <Field label="新密码" hint={passwordRuleText}>
          <PasswordInput name="newPassword" autoComplete="new-password" minLength={12} />
        </Field>
        <Field label="确认新密码">
          <PasswordInput name="confirmPassword" autoComplete="new-password" minLength={12} />
        </Field>
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} onClick={onClose} disabled={saving}>
            取消
          </button>
          <button className={`${styles.setBtn} primary`} disabled={saving}>
            {saving ? "正在保存…" : "保存新密码"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}

// PasswordInput is a password field with a button that shows what was typed.
export function PasswordInput({
  name,
  autoComplete,
  minLength,
  autoFocus,
}: {
  name: string;
  autoComplete: "current-password" | "new-password";
  minLength?: number;
  autoFocus?: boolean;
}) {
  const [shown, setShown] = useState(false);
  return (
    <span className={styles.setPassword}>
      <input
        className={styles.setInput}
        name={name}
        type={shown ? "text" : "password"}
        minLength={minLength}
        required
        autoComplete={autoComplete}
        autoFocus={autoFocus}
        spellCheck={false}
      />
      <button
        type="button"
        aria-label={shown ? "隐藏密码" : "显示密码"}
        aria-pressed={shown}
        onClick={() => setShown((value) => !value)}
      >
        {shown ? <EyeOff /> : <Eye />}
      </button>
    </span>
  );
}

function formatDateTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(
        date,
      );
}
