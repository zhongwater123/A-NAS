import { describe, expect, it } from "vitest";

import { dayLabel, describeActivity, describeGroup, groupActivity, originLabel, plainText } from "./describe";
import type { ActivityEvent } from "./logsApi";

const owner = { id: "user:owner", username: "owner" };
const alice = { id: "user:alice", username: "alice" };

function event(overrides: Partial<ActivityEvent>): ActivityEvent {
  return { id: "audit:1", occurredAt: "2026-10-12T06:00:00Z", category: "file", action: "file.uploaded", resourceType: "file", ...overrides };
}

const sentence = (value: ActivityEvent, viewer = owner.id) => plainText(describeActivity(value, viewer).parts);

describe("activity sentences", () => {
  it("describes sign-ins with where they came from", () => {
    const signedIn = event({ category: "session", action: "session.signed_in", actor: alice, subject: alice, detail: '{"via":"lan","address":"172.18.45.12"}' });
    expect(sentence(signedIn)).toBe("alice 登录了");
    expect(describeActivity(signedIn, owner.id).meta).toEqual(["局域网 · 172.18.45.12"]);
    expect(sentence(signedIn, alice.id)).toBe("你 登录了");
    expect(originLabel('{"via":"local-console"}')).toBe("本机屏幕");
    expect(originLabel('{"via":"loopback"}')).toContain("SSH 隧道");
  });

  it("flags failed sign-ins, naming an unknown account that was tried", () => {
    const wrong = describeActivity(event({ category: "session", action: "session.sign_in_failed", actor: alice, detail: '{"via":"lan","address":"10.0.0.9","reason":"invalid_password"}' }), owner.id);
    expect(plainText(wrong.parts)).toBe("alice 登录失败");
    expect(wrong.meta).toEqual(["密码错误", "局域网 · 10.0.0.9"]);
    expect(wrong.tone).toBe("warning");
    const unknown = event({ category: "session", action: "session.sign_in_failed", detail: '{"via":"lan","username":"root","reason":"unknown_user"}' });
    expect(sentence(unknown)).toBe("有人尝试以 root 登录，失败");
    expect(describeActivity(unknown, owner.id).meta[0]).toBe("账号不存在");
  });

  it("describes account changes and administrative viewing", () => {
    expect(sentence(event({ category: "account", action: "user.credential_reset", actor: owner, subject: alice }))).toBe("你 重置了 alice 的密码");
    expect(sentence(event({ category: "account", action: "user.created", actor: owner, subject: alice, detail: "member" }))).toBe("你 添加了成员 alice");
    expect(sentence(event({ category: "account", action: "user.created", subject: owner, detail: "admin" }), alice.id)).toBe("设备初始化，创建了管理员 owner");
    const viewing = event({
      category: "account", action: "space.viewing_started", actor: owner, resourceType: "space",
      space: { id: "space:alice", kind: "private", owner: alice }, detail: '{"reason":"找回照片","expiresAt":"2026-10-13T06:00:00Z"}',
    });
    expect(plainText(describeActivity(viewing, alice.id).parts)).toBe("owner 开始只读查看 你 的个人空间");
    expect(describeActivity(viewing, alice.id).meta[0]).toBe("原因：找回照片");
  });

  it("names files in visible spaces and only counts hidden ones", () => {
    const shared = event({ actor: alice, detail: "家庭旅行.pdf", space: { id: "space:shared", kind: "shared" } });
    expect(sentence(shared)).toBe("alice 上传了 家庭旅行.pdf");
    expect(describeActivity(shared, owner.id).meta).toEqual(["共享空间"]);
    const hidden = event({ actor: alice, redacted: true, space: { id: "space:alice", kind: "private", owner: alice } });
    expect(sentence(hidden)).toBe("alice 在个人空间上传了 1 个文件");
    const purged = event({ action: "file.purged.retention", system: true, redacted: true, space: { id: "space:alice", kind: "private", owner: alice } });
    expect(sentence(purged)).toBe("系统 在 alice 的个人空间清除了回收站中 1 个到期项目");
  });

  it("describes storage plans and unknown actions", () => {
    const failed = describeActivity(event({ category: "storage", action: "storage.plan.failed", system: true, detail: "mkfs.btrfs failed" }), owner.id);
    expect(plainText(failed.parts)).toBe("系统 数据卷初始化失败");
    expect(failed.tone).toBe("danger");
    expect(failed.meta).toEqual(["mkfs.btrfs failed"]);
    expect(sentence(event({ category: "other", action: "backup.started", actor: owner }))).toBe("你 执行了 backup.started");
  });
});

describe("grouping", () => {
  it("merges a run of uploads by one account within minutes", () => {
    const uploads = ["c.jpg", "b.jpg", "a.jpg", "d.jpg"].map((name, index) => event({
      id: `audit:${index}`, actor: alice, detail: name, occurredAt: new Date(Date.parse("2026-10-12T06:10:00Z") - index * 60_000).toISOString(),
      space: { id: "space:shared", kind: "shared" },
    }));
    const signIn = event({ id: "audit:s", category: "session", action: "session.signed_in", actor: alice, occurredAt: "2026-10-12T06:00:00Z" });
    const groups = groupActivity([...uploads, signIn]);
    expect(groups.map((group) => group.events.length)).toEqual([4, 1]);
    const described = describeGroup(groups[0], owner.id);
    expect(plainText(described.parts)).toBe("alice 上传了 4 个文件");
    expect(described.meta).toEqual(["共享空间", "c.jpg、b.jpg、a.jpg 等"]);
  });

  it("keeps runs apart across accounts, spaces and long pauses", () => {
    const at = (minutes: number) => new Date(Date.parse("2026-10-12T06:00:00Z") - minutes * 60_000).toISOString();
    const groups = groupActivity([
      event({ id: "1", actor: alice, occurredAt: at(0), detail: "a" }),
      event({ id: "2", actor: owner, occurredAt: at(1), detail: "b" }),
      event({ id: "3", actor: owner, occurredAt: at(20), detail: "c" }),
      event({ id: "4", actor: owner, occurredAt: at(21), detail: "d", space: { id: "space:shared", kind: "shared" } }),
    ]);
    expect(groups.map((group) => group.events.map((item) => item.id))).toEqual([["1"], ["2"], ["3"], ["4"]]);
  });
});

it("labels days relative to today", () => {
  const now = new Date(2026, 9, 12, 15, 0);
  expect(dayLabel(new Date(2026, 9, 12, 9, 0), now)).toBe("今天 · 10月12日 星期一");
  expect(dayLabel(new Date(2026, 9, 11, 9, 0), now)).toBe("昨天 · 10月11日 星期日");
  expect(dayLabel(new Date(2026, 9, 9, 9, 0), now)).toBe("10月9日 星期五");
  expect(dayLabel(new Date(2025, 9, 9, 9, 0), now)).toBe("2025年10月9日 星期四");
});
