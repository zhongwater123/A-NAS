import { useEffect, useState } from "react";

import { relativeTime } from "../lib/time";
import {
  addWallMessage,
  approveJoin,
  deleteComment,
  type FamilyBaby,
  type FamilyComment,
  type FamilyMember,
  familyError,
  leaveFamily,
  listWall,
  rejectJoin,
  removeMember,
  requestKey,
  setGuardian,
  setRole,
} from "./api";
import { canDelete, useFamily } from "./context";
import styles from "./family.module.css";
import { ItemMenu, MemberFace, type MenuItem, useMemberName } from "./parts";

// Members is 家人 (亲友团): who asks to join, who is in the family, and the
// letters the family leaves for the baby to read when grown.
export function Members({ baby }: { baby: FamilyBaby }) {
  const family = useFamily();
  const nameOf = useMemberName();
  const { overview } = family;
  const run = async (work: () => Promise<unknown>, done?: string) => {
    try {
      await work();
      await family.reload();
      if (done) family.toast({ text: done, tone: "done" });
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };

  const menu = (member: FamilyMember): MenuItem[] => {
    const name = nameOf(member.accountId, member.username).name;
    if (member.accountId === family.userId) {
      return [
        {
          label: "修改我的称呼",
          run: () =>
            void family
              .prompt({ title: "修改我的称呼", label: "你在家里是", value: member.role, maxLength: 8 })
              .then((role) => {
                if (role?.trim()) void run(() => setRole(role.trim()));
              }),
        },
        {
          label: "退出家庭",
          danger: true,
          run: () =>
            void family
              .confirm({
                title: "退出家庭？",
                message: "你添加的内容会留在频道里。之后可以再申请加入。",
                confirm: "退出",
                danger: true,
              })
              .then((ok) => ok && void run(leaveFamily)),
        },
      ];
    }
    if (!overview.me.guardian) return [];
    return [
      member.guardian
        ? { label: "改为亲友", run: () => void run(() => setGuardian(member.accountId, false), `${name}现在是亲友`) }
        : { label: "设为家长", run: () => void run(() => setGuardian(member.accountId, true), `${name}现在是家长`) },
      {
        label: "移出家庭",
        danger: true,
        run: () =>
          void family
            .confirm({
              title: `把${name}移出家庭？`,
              message: "TA 添加的照片、记录和留言都会保留。",
              confirm: "移出",
              danger: true,
            })
            .then((ok) => ok && void run(() => removeMember(member.accountId))),
      },
    ];
  };

  return (
    <div className={styles.fcMembers}>
      <header className={styles.fcBoardHead}>
        <div>
          <h2>家人</h2>
          <p>家长可以添加宝宝、修改宝宝资料、同意加入、删除任何人的内容；亲友可以记录和留言，修改自己添加的内容。</p>
        </div>
      </header>

      {overview.requests.length > 0 && (
        <section className={styles.fcBoardSection}>
          <h3>申请加入</h3>
          <ul className={styles.fcRows}>
            {overview.requests.map((request) => (
              <li key={request.accountId}>
                <MemberFace accountId={request.accountId} username={request.username} className={styles.fcFace} />
                <span className={styles.fcRowMain}>
                  {nameOf(request.accountId, request.username).name}
                  <small>
                    想以“{request.role}”的身份加入 · {relativeTime(request.requestedAt)}
                  </small>
                </span>
                <button
                  type="button"
                  className={styles.fcSecondary}
                  onClick={() => void run(() => rejectJoin(request.accountId))}
                >
                  拒绝
                </button>
                <button
                  type="button"
                  className={styles.fcPrimary}
                  onClick={() => void run(() => approveJoin(request.accountId), `${request.role}已加入家庭`)}
                >
                  同意
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <ul className={styles.fcPeople} aria-label="家人">
        {overview.members.map((member) => {
          const who = nameOf(member.accountId, member.username);
          return (
            <li key={member.accountId}>
              <MemberFace accountId={member.accountId} username={member.username} className={styles.fcFaceLarge} />
              <strong>{member.role}</strong>
              <span className={styles.fcPeopleWho}>
                {who.name}
                {member.guardian && <em>家长</em>}
              </span>
              <div className={styles.fcPeopleMenu}>
                <ItemMenu label={`${who.name}的操作`} items={menu(member)} />
              </div>
            </li>
          );
        })}
      </ul>

      <Letters baby={baby} />
    </div>
  );
}

// Letters is the message wall: what the family writes to the baby, newest
// first, to read when grown.
function Letters({ baby }: { baby: FamilyBaby }) {
  const family = useFamily();
  const { toast } = family;
  const nameOf = useMemberName();
  const [messages, setMessages] = useState<FamilyComment[]>();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const name = baby.nickname || baby.name;

  useEffect(() => {
    let live = true;
    listWall(baby.id).then(
      (items) => {
        if (live) setMessages(items);
      },
      (caught: unknown) => {
        if (live) toast({ text: familyError(caught), tone: "error" });
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, toast]);

  const send = async () => {
    const value = text.trim();
    if (!value || busy) return;
    setBusy(true);
    try {
      const message = await addWallMessage(baby.id, value, requestKey());
      setMessages((current) => [message, ...(current ?? [])]);
      setText("");
    } catch (caught) {
      toast({ text: familyError(caught), tone: "error" });
    }
    setBusy(false);
  };

  return (
    <section className={styles.fcBoardSection}>
      <h3>写给{name}的话</h3>
      <form
        className={styles.fcLetterForm}
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
      >
        <textarea
          className={styles.fcTextarea}
          rows={3}
          maxLength={1000}
          placeholder={`写给${name}的话，等TA长大了看`}
          value={text}
          onChange={(event) => setText(event.target.value)}
          aria-label="留言"
        />
        <button type="submit" className={styles.fcPrimary} disabled={!text.trim() || busy}>
          留言
        </button>
      </form>
      {messages === undefined && <span className="loader small" aria-label="正在读取留言" />}
      <ul className={styles.fcLetters}>
        {messages?.map((message) => {
          const who = nameOf(message.authorId, message.authorName);
          return (
            <li key={message.id}>
              <p>{message.text}</p>
              <footer>
                <MemberFace accountId={message.authorId} username={message.authorName} className={styles.fcFaceTiny} />
                <b>{who.role || who.name}</b>
                <span>{relativeTime(message.createdAt)}</span>
                <ItemMenu
                  label="留言操作"
                  items={
                    canDelete(family, message.authorId)
                      ? [
                          {
                            label: "删除留言",
                            danger: true,
                            run: () =>
                              void deleteComment(message.id).then(
                                () => setMessages((current) => current?.filter((item) => item.id !== message.id)),
                                (caught: unknown) => toast({ text: familyError(caught), tone: "error" }),
                              ),
                          },
                        ]
                      : []
                  }
                />
              </footer>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
