import { describe, expect, it } from "vitest";

import { dayLabel, describeActivity, describeGroup, groupActivity, originLabel, plainText } from "./describe";
import type { ActivityEvent } from "./logsApi";

const owner = { id: "user:owner", username: "owner" };
const alice = { id: "user:alice", username: "alice" };

function event(overrides: Partial<ActivityEvent>): ActivityEvent {
  return {
    id: "audit:1",
    occurredAt: "2026-10-12T06:00:00Z",
    category: "file",
    action: "file.uploaded",
    resourceType: "file",
    ...overrides,
  };
}

const sentence = (value: ActivityEvent, viewer = owner.id) => plainText(describeActivity(value, viewer).parts);

describe("activity sentences", () => {
  it("describes sign-ins with where they came from", () => {
    const signedIn = event({
      category: "session",
      action: "session.signed_in",
      actor: alice,
      subject: alice,
      detail: '{"via":"lan","address":"172.18.45.12"}',
    });
    expect(sentence(signedIn)).toBe("alice 登录了");
    expect(describeActivity(signedIn, owner.id).meta).toEqual(["局域网 · 172.18.45.12"]);
    expect(sentence(signedIn, alice.id)).toBe("你 登录了");
    expect(originLabel('{"via":"local-console"}')).toBe("本机屏幕");
    expect(originLabel('{"via":"loopback"}')).toContain("SSH 隧道");
  });

  it("flags failed sign-ins, naming an unknown account that was tried", () => {
    const wrong = describeActivity(
      event({
        category: "session",
        action: "session.sign_in_failed",
        actor: alice,
        detail: '{"via":"lan","address":"10.0.0.9","reason":"invalid_password"}',
      }),
      owner.id,
    );
    expect(plainText(wrong.parts)).toBe("alice 登录失败");
    expect(wrong.meta).toEqual(["密码错误", "局域网 · 10.0.0.9"]);
    expect(wrong.tone).toBe("warning");
    const unknown = event({
      category: "session",
      action: "session.sign_in_failed",
      detail: '{"via":"lan","username":"root","reason":"unknown_user"}',
    });
    expect(sentence(unknown)).toBe("有人尝试以 root 登录，失败");
    expect(describeActivity(unknown, owner.id).meta[0]).toBe("账号不存在");
  });

  it("describes account changes and administrative viewing", () => {
    expect(
      sentence(event({ category: "account", action: "user.credential_reset", actor: owner, subject: alice })),
    ).toBe("你 重置了 alice 的密码");
    expect(
      sentence(event({ category: "account", action: "user.created", actor: owner, subject: alice, detail: "member" })),
    ).toBe("你 添加了成员 alice");
    expect(
      sentence(event({ category: "account", action: "user.created", subject: owner, detail: "admin" }), alice.id),
    ).toBe("设备初始化，创建了管理员 owner");
    const viewing = event({
      category: "account",
      action: "space.viewing_started",
      actor: owner,
      resourceType: "space",
      space: { id: "space:alice", kind: "private", owner: alice },
      detail: '{"reason":"找回照片","expiresAt":"2026-10-13T06:00:00Z"}',
    });
    expect(plainText(describeActivity(viewing, alice.id).parts)).toBe("owner 开始只读查看 你 的个人空间");
    expect(describeActivity(viewing, alice.id).meta[0]).toBe("原因：找回照片");
  });

  it("names accounts by nickname and keeps the account name beside a new nickname", () => {
    const mom = { id: "user:alice", username: "alice", nickname: "妈妈" };
    expect(sentence(event({ category: "session", action: "session.signed_in", actor: mom, subject: mom }))).toBe(
      "妈妈 登录了",
    );
    const renamed = describeActivity(
      event({
        category: "account",
        action: "user.nickname_changed",
        actor: mom,
        subject: mom,
        detail: '{"from":"","to":"妈妈"}',
      }),
      owner.id,
    );
    expect(plainText(renamed.parts)).toBe("alice 把昵称改为 妈妈");
    expect(renamed.meta).toEqual(["原来显示账号名"]);
    expect(
      sentence(
        event({
          category: "account",
          action: "user.nickname_changed",
          actor: mom,
          subject: mom,
          detail: '{"from":"小爱","to":"妈妈"}',
        }),
        mom.id,
      ),
    ).toBe("你 把昵称改为 妈妈");
    expect(
      describeActivity(
        event({
          category: "account",
          action: "user.nickname_changed",
          actor: alice,
          subject: alice,
          detail: '{"from":"小爱","to":""}',
        }),
        owner.id,
      ).meta,
    ).toEqual(["原昵称：小爱"]);
    expect(
      sentence(
        event({
          category: "account",
          action: "user.nickname_changed",
          actor: alice,
          subject: alice,
          detail: '{"from":"小爱","to":""}',
        }),
      ),
    ).toBe("alice 清除了昵称");
    expect(
      sentence(event({ category: "account", action: "user.avatar_changed", actor: mom, subject: mom }), mom.id),
    ).toBe("你 更换了头像");
    const viewing = event({
      category: "account",
      action: "space.viewing_started",
      actor: owner,
      resourceType: "space",
      space: { id: "space:alice", kind: "private", owner: mom },
    });
    expect(sentence(viewing)).toBe("你 开始只读查看 妈妈 的个人空间");
  });

  it("names files in visible spaces and only counts hidden ones", () => {
    const shared = event({ actor: alice, detail: "家庭旅行.pdf", space: { id: "space:shared", kind: "shared" } });
    expect(sentence(shared)).toBe("alice 上传了 家庭旅行.pdf");
    expect(describeActivity(shared, owner.id).meta).toEqual(["共享空间"]);
    const hidden = event({ actor: alice, redacted: true, space: { id: "space:alice", kind: "private", owner: alice } });
    expect(sentence(hidden)).toBe("alice 在个人空间上传了 1 个文件");
    const purged = event({
      action: "file.purged.retention",
      system: true,
      redacted: true,
      space: { id: "space:alice", kind: "private", owner: alice },
    });
    expect(sentence(purged)).toBe("系统 在 alice 的个人空间清除了回收站中 1 个到期项目");
    const replaced = event({
      actor: alice,
      detail: "行程单.xlsx (replaced)",
      space: { id: "space:shared", kind: "shared" },
    });
    expect(sentence(replaced)).toBe("alice 上传了 行程单.xlsx");
    expect(describeActivity(replaced, owner.id).meta).toEqual(["共享空间", "替换了同名文件"]);
    const restored = event({
      action: "file.restored",
      actor: alice,
      detail: "计划.docx (replaced)",
      space: { id: "space:shared", kind: "shared" },
    });
    expect(sentence(restored)).toBe("alice 从回收站恢复了 计划.docx");
    expect(describeActivity(restored, owner.id).meta).toEqual(["共享空间", "替换了同名文件"]);
    expect(
      sentence(event({ category: "photo", action: "photo.ai_label_hidden", actor: alice, detail: "cat" }), alice.id),
    ).toBe("你 从 AI 聚合中移出了 1 张照片");
  });

  it("describes scheduled snapshots", () => {
    const shared = { id: "space:shared", kind: "shared" as const };
    const weekly = event({
      action: "snapshot.schedule_updated",
      actor: owner,
      detail: "weekly 03:30 weekday=1 keep=4",
      space: shared,
    });
    expect(sentence(weekly)).toBe("你 修改了定时快照");
    expect(describeActivity(weekly, owner.id).meta).toEqual(["共享空间", "每周一 03:30 · 保留最近 4 份"]);
    expect(
      describeActivity(
        event({ action: "snapshot.schedule_updated", actor: owner, detail: "hourly 00:15 weekday=0 keep=24" }),
        owner.id,
      ).meta,
    ).toEqual(["每小时第 15 分 · 保留最近 24 份"]);
    expect(
      describeActivity(event({ action: "snapshot.schedule_updated", actor: owner, detail: "disabled" }), owner.id).meta,
    ).toEqual(["已关闭定时快照"]);
    expect(
      sentence(event({ action: "snapshot.expired", system: true, detail: "定时快照 2026-10-12 03:00", space: shared })),
    ).toBe("系统 清理了到期的定时快照 定时快照 2026-10-12 03:00");
    expect(
      sentence(event({ action: "snapshot.missing", system: true, detail: "定时快照 2026-10-12 03:00", space: shared })),
    ).toBe("系统 移除了数据卷上已不存在的快照 定时快照 2026-10-12 03:00");
    const unrecorded = describeActivity(event({ action: "snapshot.unrecorded", system: true, detail: "3" }), owner.id);
    expect(plainText(unrecorded.parts)).toBe("系统 保留了 3 份 快照目录中没有记录的快照");
    expect(unrecorded.tone).toBe("warning");
    expect(sentence(event({ action: "snapshot.unfound", system: true, detail: "2" }))).toBe(
      "系统 保留了 2 条 快照记录，数据卷上一份对应的快照都没有找到",
    );
    expect(sentence(event({ action: "snapshot.folder_restored", actor: alice, detail: "snapshot:1" }))).toBe(
      "alice 从快照恢复了 1 个文件夹",
    );
  });

  it("describes storage plans and unknown actions", () => {
    const failed = describeActivity(
      event({ category: "storage", action: "storage.plan.failed", system: true, detail: "mkfs.btrfs failed" }),
      owner.id,
    );
    expect(plainText(failed.parts)).toBe("系统 数据卷初始化失败");
    expect(failed.tone).toBe("danger");
    expect(failed.meta).toEqual(["mkfs.btrfs failed"]);
    expect(sentence(event({ category: "other", action: "backup.started", actor: owner }))).toBe(
      "你 执行了 backup.started",
    );
  });
});

describe("grouping", () => {
  it("merges a run of uploads by one account within minutes", () => {
    const uploads = ["c.jpg", "b.jpg", "a.jpg", "d.jpg"].map((name, index) =>
      event({
        id: `audit:${index}`,
        actor: alice,
        detail: name,
        occurredAt: new Date(Date.parse("2026-10-12T06:10:00Z") - index * 60_000).toISOString(),
        space: { id: "space:shared", kind: "shared" },
      }),
    );
    const signIn = event({
      id: "audit:s",
      category: "session",
      action: "session.signed_in",
      actor: alice,
      occurredAt: "2026-10-12T06:00:00Z",
    });
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
