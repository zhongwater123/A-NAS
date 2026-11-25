import { FolderLock, Images, KeyRound, type LucideIcon, Trash2, Undo2, UserCheck, UserPlus, UserX } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";

import type { DeletionChoice, User } from "../api.generated";
import { passwordProblem, passwordRuleText, usernameProblem, usernameRuleText } from "../auth/accountRules";
import { calendarDate } from "../lib/time";
import { displayName, fullName } from "../people/people";
import { PasswordInput } from "./AccountSection";
import {
  cancelDeletion,
  createMember,
  disableMember,
  enableMember,
  listUsers,
  resetMember,
  scheduleDeletion,
  startViewing,
  type ViewingScope,
} from "./api";
import { roleLabel, statusLabel } from "./labels";
import {
  Avatar,
  Banner,
  Field,
  Group,
  IconTile,
  type MenuItem,
  Modal,
  messageOf,
  PageHeader,
  Pill,
  RowMenu,
  Segmented,
  useSettingsKit,
} from "./parts";
import styles from "./settings.module.css";

type UserDialog =
  | { kind: "create" }
  | { kind: "reset"; user: User }
  | { kind: "viewing"; user: User; scope: ViewingScope }
  | { kind: "disable"; user: User }
  | { kind: "enable"; user: User }
  | { kind: "delete"; user: User }
  | { kind: "cancelDeletion"; user: User };

// A deleted account waits this long, disabled and unchanged, before its data
// is removed (docs/specs/account-enable-and-delete.md).
const deletionDays = 30;

// UsersSection is Settings → 用户与权限, shown to administrators only: the
// household's accounts and what an administrator may do to each
// (docs/specs/unified-identity-and-file-acl.md).
export function UsersSection({
  currentUser,
  onChangeOwnPassword,
}: {
  currentUser: User;
  onChangeOwnPassword: () => void;
}) {
  const { toast } = useSettingsKit();
  const [users, setUsers] = useState<User[]>();
  const [error, setError] = useState("");
  const [dialog, setDialog] = useState<UserDialog>();
  const refresh = useCallback(
    () =>
      listUsers()
        .then((items) => {
          setUsers(items);
          setError("");
        })
        .catch((caught: unknown) => {
          setError(messageOf(caught));
        }),
    [],
  );
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const finish = async (text: string) => {
    setDialog(undefined);
    toast({ text });
    await refresh();
  };
  return (
    <>
      <PageHeader
        title="用户与权限"
        action={
          <button
            className={`${styles.setBtn} primary`}
            data-setting="add-member"
            onClick={() => setDialog({ kind: "create" })}
          >
            <UserPlus />
            添加成员
          </button>
        }
      />
      {error && <Banner tone="error">{error}</Banner>}
      {/* Administrators do not see members' personal spaces or private
          galleries. Read-only access from a member's menu needs a reason, is
          audited, notifies the member and ends after 24 hours; its dialog says so. */}
      <Group title="账号" aside={users ? `${users.length} 个` : undefined} anchor="members">
        {users === undefined ? (
          <div className={`${styles.setState} ${styles.compact}`}>
            <span className="loader" />
            <p>正在读取账号…</p>
          </div>
        ) : (
          sortUsers(users, currentUser.id).map((user) => {
            const self = user.id === currentUser.id;
            return (
              <div className={`${styles.setRow} ${styles.userRow}`} key={user.id}>
                <Avatar who={user} />
                <div className={styles.setRowText}>
                  <span>
                    <strong>{displayName(user)}</strong>
                    {user.nickname && <span className={styles.setHandle}>{user.username}</span>}
                    {self && <Pill tone="accent">你</Pill>}
                  </span>
                  <small>
                    {roleLabel(user.role)}
                    {user.deletion ? (
                      <span className={`${styles.acctState} ${styles["status-deleting"] ?? ""}`}>
                        {deletionLabel(user.deletion)}
                      </span>
                    ) : (
                      user.status !== "active" && (
                        <span className={`${styles.acctState} ${styles[`status-${user.status}`] ?? ""}`}>
                          {statusLabel(user.status)}
                        </span>
                      )
                    )}
                  </small>
                </div>
                <div className={styles.setRowEnd}>
                  {self ? (
                    <button className={styles.setBtn} onClick={onChangeOwnPassword}>
                      修改我的密码
                    </button>
                  ) : (
                    <RowMenu label={`${fullName(user)} 的更多操作`} items={menuOf(user, setDialog)} />
                  )}
                </div>
              </div>
            );
          })
        )}
      </Group>
      {dialog && (
        <UserDialogView
          dialog={dialog}
          onClose={() => setDialog(undefined)}
          onDone={(message) => void finish(message)}
        />
      )}
    </>
  );
}

// deletionLabel says where a deleted account stands.
function deletionLabel(deletion: NonNullable<User["deletion"]>): string {
  if (deletion.retrying) return "删除未完成，将自动重试";
  if (deletion.started) return "正在删除";
  return `待删除 · ${calendarDate(deletion.dueAt)}删除`;
}

// menuOf lists what an administrator may do to another account in its state.
function menuOf(user: User, open: (dialog: UserDialog) => void): MenuItem[] {
  const viewing: MenuItem[] = [
    { label: "查看个人空间…", icon: FolderLock, onSelect: () => open({ kind: "viewing", user, scope: "space" }) },
    { label: "查看私有图库…", icon: Images, onSelect: () => open({ kind: "viewing", user, scope: "library" }) },
  ];
  if (user.deletion?.started) return viewing.map((item) => (item === "separator" ? item : { ...item, disabled: true }));
  if (user.deletion) {
    return [
      ...viewing,
      "separator",
      { label: "撤销删除…", icon: Undo2, onSelect: () => open({ kind: "cancelDeletion", user }) },
    ];
  }
  const reset: MenuItem = { label: "重置密码…", icon: KeyRound, onSelect: () => open({ kind: "reset", user }) };
  if (user.status === "disabled") {
    return [
      reset,
      ...viewing,
      "separator",
      { label: "重新启用…", icon: UserCheck, onSelect: () => open({ kind: "enable", user }) },
      { label: "删除账号…", icon: Trash2, danger: true, onSelect: () => open({ kind: "delete", user }) },
    ];
  }
  return [
    reset,
    ...viewing,
    "separator",
    { label: "禁用账号…", icon: UserX, danger: true, onSelect: () => open({ kind: "disable", user }) },
  ];
}

const deletionChoices = [
  { value: "transfer", label: "转给我" },
  { value: "delete", label: "一并删除" },
] as const;

function UserDialogView({
  dialog,
  onClose,
  onDone,
}: {
  dialog: UserDialog;
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [personalSpace, setPersonalSpace] = useState<DeletionChoice>("transfer");
  const [library, setLibrary] = useState<DeletionChoice>("transfer");
  const [dueAt] = useState(() => Date.now() + deletionDays * 86_400_000);
  const run = async (action: () => Promise<string>) => {
    setBusy(true);
    setError("");
    try {
      onDone(await action());
    } catch (caught) {
      setError(messageOf(caught));
      setBusy(false);
    }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const value = (name: string) => String(data.get(name));
    const problem =
      dialog.kind === "create"
        ? (usernameProblem(value("username")) ?? passwordProblem(value("password")))
        : dialog.kind === "reset"
          ? passwordProblem(value("password"))
          : undefined;
    if (problem) {
      setError(problem);
      return;
    }
    if (dialog.kind === "create") {
      void run(async () => {
        const member = await createMember(value("username"), value("password"));
        return `已添加成员 ${member.username}。`;
      });
    } else if (dialog.kind === "reset") {
      void run(async () => {
        await resetMember(dialog.user.id, value("password"));
        return `已重置 ${fullName(dialog.user)} 的密码。对方下次登录时必须先设置新密码，SMB 共享在此之前保持停用。`;
      });
    } else if (dialog.kind === "viewing") {
      const { user, scope } = dialog;
      void run(async () => {
        await startViewing(user.id, value("password"), value("reason"), scope);
        return scope === "library"
          ? `已开启对 ${fullName(user)} 私有图库的只读查看，可在相册中选择“只读查看 · ${displayName(user)}”。`
          : `已开启对 ${fullName(user)} 个人空间的只读查看，可在文件管理中选择“只读查看 · ${displayName(user)}”。`;
      });
    } else if (dialog.kind === "enable") {
      void run(async () => {
        await enableMember(dialog.user.id);
        return `已重新启用 ${fullName(dialog.user)}。`;
      });
    } else if (dialog.kind === "delete") {
      void run(async () => {
        const deleted = await scheduleDeletion(dialog.user.id, value("password"), personalSpace, library);
        const due = deleted.deletion ? calendarDate(deleted.deletion.dueAt) : `${deletionDays} 天后`;
        return `${fullName(dialog.user)} 将于${due}删除，在此之前可以撤销。`;
      });
    } else if (dialog.kind === "cancelDeletion") {
      void run(async () => {
        await cancelDeletion(dialog.user.id);
        return `已撤销删除 ${fullName(dialog.user)}，账号仍为禁用。`;
      });
    } else {
      void run(async () => {
        await disableMember(dialog.user.id);
        return `已禁用 ${fullName(dialog.user)}。`;
      });
    }
  };
  const what = dialog.kind === "viewing" && dialog.scope === "library" ? "私有图库" : "个人空间";
  const content = {
    create: {
      title: "添加成员",
      label: "添加成员",
      icon: UserPlus,
      tone: "accent",
      description: "成员拥有自己的个人空间和私有图库，并可使用 Shared 共享空间。",
      submit: "创建成员",
      danger: false,
    },
    reset: dialog.kind === "reset" && {
      title: `重置 ${fullName(dialog.user)} 的密码`,
      label: `重置 ${fullName(dialog.user)} 的密码`,
      icon: KeyRound,
      tone: "warning",
      description: `重置后 ${displayName(dialog.user)} 下次登录必须先设置新密码，在此之前 SMB 共享保持停用。重置会写入审计，并一直显示在对方的“我的账号”中。`,
      submit: "确认重置",
      danger: false,
    },
    viewing: dialog.kind === "viewing" && {
      title: `只读查看 ${fullName(dialog.user)} 的${what}`,
      label: `查看 ${fullName(dialog.user)} 的${what}`,
      icon: what === "私有图库" ? Images : FolderLock,
      tone: "warning",
      description: `查看他人${what}会写入审计，并在 ${displayName(dialog.user)} 下次登录时通知对方。访问为只读，24 小时后自动结束。`,
      submit: "开始只读查看",
      danger: false,
    },
    disable: dialog.kind === "disable" && {
      title: `禁用 ${fullName(dialog.user)}？`,
      label: `禁用 ${fullName(dialog.user)}`,
      icon: UserX,
      tone: "danger",
      description: `${displayName(dialog.user)} 将无法登录网页和 SMB 共享，已建立的 SMB 连接会立即断开，发出的分享会撤销；个人空间中的文件不会被删除。之后可以重新启用。`,
      submit: "禁用账号",
      danger: true,
    },
    enable: dialog.kind === "enable" && {
      title: `重新启用 ${fullName(dialog.user)}？`,
      label: `重新启用 ${fullName(dialog.user)}`,
      icon: UserCheck,
      tone: "accent",
      description: `${displayName(dialog.user)} 可以用原来的密码登录网页和 SMB 共享，AI 助手随之恢复。禁用时撤销的分享不会恢复。`,
      submit: "重新启用",
      danger: false,
    },
    delete: dialog.kind === "delete" && {
      title: `删除 ${fullName(dialog.user)}？`,
      label: `删除 ${fullName(dialog.user)}`,
      icon: Trash2,
      tone: "danger",
      description: `账号保持禁用 ${deletionDays} 天，${calendarDate(dueAt)}彻底删除，在此之前可以撤销。到期后选了“转给我”的内容转入你的名下，其余数据删除，无法恢复；账号名之后可以重新使用。`,
      submit: "删除账号",
      danger: true,
    },
    cancelDeletion: dialog.kind === "cancelDeletion" && {
      title: `撤销删除 ${fullName(dialog.user)}？`,
      label: `撤销删除 ${fullName(dialog.user)}`,
      icon: Undo2,
      tone: "accent",
      description: "账号不再删除，仍为禁用，数据保持原样。之后可以重新启用。",
      submit: "撤销删除",
      danger: false,
    },
  }[dialog.kind] as {
    title: string;
    label: string;
    icon: LucideIcon;
    tone: string;
    description: string;
    submit: string;
    danger: boolean;
  };
  return (
    <Modal
      title={content.title}
      label={content.title}
      description={content.description}
      icon={<IconTile tone={content.tone} icon={content.icon} size="lg" />}
      busy={busy}
      onClose={onClose}
    >
      <form className={styles.setDialogForm} aria-label={content.label} autoComplete="off" onSubmit={submit}>
        {dialog.kind === "create" && (
          <>
            <Field label="账号" hint={usernameRuleText}>
              <input
                className={styles.setInput}
                name="username"
                required
                autoFocus
                autoCapitalize="none"
                spellCheck={false}
              />
            </Field>
            <Field label="初始密码" hint={`${passwordRuleText}；成员可在“我的账号”中自行修改`}>
              <PasswordInput name="password" autoComplete="new-password" minLength={12} />
            </Field>
          </>
        )}
        {dialog.kind === "reset" && (
          <Field label="新密码" hint={passwordRuleText}>
            <PasswordInput name="password" autoComplete="new-password" minLength={12} autoFocus />
          </Field>
        )}
        {dialog.kind === "delete" && (
          <>
            <Field
              group
              label="个人空间"
              hint={
                personalSpace === "transfer"
                  ? `放进你个人空间的“${dialog.user.username} 的文件”文件夹`
                  : "连同回收站、快照一起删除"
              }
            >
              <Segmented label="个人空间" value={personalSpace} choices={deletionChoices} onChange={setPersonalSpace} />
            </Field>
            <Field
              group
              label="私有图库"
              hint={
                library === "transfer"
                  ? `放进你私有图库的“${dialog.user.username} 的照片”相册`
                  : "连同图库回收站一起删除"
              }
            >
              <Segmented label="私有图库" value={library} choices={deletionChoices} onChange={setLibrary} />
            </Field>
            <Field label="你的密码" hint="为确认是你本人操作">
              <PasswordInput name="password" autoComplete="current-password" />
            </Field>
          </>
        )}
        {dialog.kind === "viewing" && (
          <>
            <Field label="查看原因">
              <input
                className={styles.setInput}
                name="reason"
                maxLength={500}
                required
                autoFocus
                placeholder="例如：帮助找回误删的照片"
              />
            </Field>
            <Field label="你的密码" hint="为确认是你本人操作">
              <PasswordInput name="password" autoComplete="current-password" />
            </Field>
          </>
        )}
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className={styles.setBtn} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className={`${styles.setBtn} ${content.danger ? styles.danger : "primary"}`} disabled={busy}>
            {busy ? "正在处理…" : content.submit}
          </button>
        </footer>
      </form>
    </Modal>
  );
}

// The signed-in account comes first, then administrators, then members by name.
function sortUsers(users: User[], selfID: string): User[] {
  const rank = (user: User) => (user.id === selfID ? 0 : user.role === "admin" ? 1 : 2);
  return [...users].sort(
    (left, right) => rank(left) - rank(right) || displayName(left).localeCompare(displayName(right), "zh-CN"),
  );
}
