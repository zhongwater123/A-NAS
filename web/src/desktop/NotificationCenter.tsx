import { useEffect, useState } from "react";

import type { Notification } from "../api.generated";
import { acknowledgeNotification, listNotifications } from "./api";
import { fullName, usePeople, type People } from "../people/people";

// NotificationCenter shows what administrators did to the account since the
// user last acknowledged it: viewing the private space or resetting the password.
// Runs of VNAS's scheduled tasks are toasts of their own (TaskToasts).
export function NotificationCenter() {
  const people = usePeople();
  const [items, setItems] = useState<Notification[]>([]);
  useEffect(() => {
    void listNotifications()
      .then((list) => setItems(list.filter((item) => item.kind !== "assistant_task")))
      .catch(() => undefined);
  }, []);
  if (!items.length) return null;
  const acknowledge = async () => {
    await Promise.allSettled(items.map((item) => acknowledgeNotification(item.id)));
    setItems([]);
  };
  return (
    <div className="notice-layer">
      <section className="notice-card" role="alertdialog" aria-label="账号通知">
        <p className="section-label">ACCOUNT NOTICE</p>
        <h2>账号通知</h2>
        <ul>
          {items.map((item) => (
            <li key={item.id}>{describeNotification(item, people)}</li>
          ))}
        </ul>
        <button className="primary" onClick={() => void acknowledge()}>
          知道了
        </button>
      </section>
    </div>
  );
}

function describeNotification(item: Notification, people: People): string {
  const when = new Date(item.createdAt).toLocaleString("zh-CN");
  // What an administrator did names them with the account name too.
  const actor = fullName(people.byUsername(item.actorUsername) ?? { username: item.actorUsername });
  if (item.kind === "admin_viewing" || item.kind === "admin_library_viewing") {
    const what = item.kind === "admin_library_viewing" ? "私有图库" : "个人空间";
    const until = item.expiresAt ? `，${new Date(item.expiresAt).toLocaleString("zh-CN")} 自动结束` : "";
    return `管理员 ${actor} 于 ${when} 开启了对你${what}的只读查看${until}。原因：${item.reason ?? "未填写"}`;
  }
  return `管理员 ${actor} 于 ${when} 重置了你的密码。`;
}
