import { describe, expect, it } from "vitest";

import { ContainerImage, ContainerSummary } from "../containersApi";
import {
  AppRef,
  appProject,
  describeContainer,
  groupContainers,
  imageUsers,
  matchesFilter,
  matchesQuery,
  splitReference,
  webPorts,
} from "./model";

function container(name: string, overrides: Partial<ContainerSummary> = {}): ContainerSummary {
  return {
    id: name.padEnd(64, "0"),
    name,
    image: `${name}:1`,
    state: "running",
    status: "Up 3 hours",
    createdAt: "2026-10-06T00:00:00Z",
    ports: [],
    ...overrides,
  };
}

const immich: AppRef = { id: "immich", title: "Immich", webPort: 2283, scheme: "http", path: "/" };
const apps = new Map([["immich", immich]]);

describe("describeContainer", () => {
  it.each([
    [{ state: "running", status: "Up 3 hours" }, "运行中", "ok", "已运行 3 小时"],
    [{ state: "running", status: "Up Less than a second" }, "运行中", "ok", "刚刚启动"],
    [{ state: "running", status: "Up About an hour (healthy)" }, "运行中", "ok", "已运行约 1 小时"],
    [{ state: "running", status: "Up 2 days (unhealthy)" }, "不健康", "danger", "已运行 2 天"],
    [{ state: "running", status: "Up 8 seconds (health: starting)" }, "启动中", "warn", "已运行 8 秒"],
    [{ state: "paused", status: "Up 2 weeks (Paused)" }, "已暂停", "warn", "已运行 2 周，已暂停"],
    [{ state: "exited", status: "Exited (0) 5 hours ago" }, "已停止", "idle", "5 小时前停止"],
    [{ state: "exited", status: "Exited (143) About a minute ago" }, "已停止", "idle", "约 1 分钟前停止 · 退出码 143"],
    [{ state: "exited", status: "Exited (1) 2 days ago" }, "异常退出", "danger", "2 天前停止 · 退出码 1"],
    [{ state: "restarting", status: "Restarting (1) 5 seconds ago" }, "重启中", "warn", "5 秒前退出 · 退出码 1"],
    [{ state: "created", status: "Created" }, "未启动", "idle", "创建后还没有启动过"],
    [{ state: "dead", status: "Dead" }, "异常", "danger", "引擎没能停止或删除这个容器"],
  ] as const)("reads %o as %s", (fields, label, tone, detail) => {
    const condition = describeContainer(container("app", fields));
    expect(condition).toMatchObject({ label, tone, detail });
  });

  it("keeps the engine's text for a status it does not know", () => {
    expect(describeContainer(container("app", { status: "Up for a while" })).detail).toBe("Up for a while");
  });

  it("asks for attention only for failures", () => {
    const attention = (fields: Partial<ContainerSummary>) => describeContainer(container("app", fields)).attention;
    expect(attention({ status: "Up 2 days (unhealthy)" })).toBe(true);
    expect(attention({ state: "exited", status: "Exited (1) 2 days ago" })).toBe(true);
    expect(attention({ state: "restarting", status: "Restarting (1) 5 seconds ago" })).toBe(true);
    expect(attention({ state: "exited", status: "Exited (137) 2 days ago" })).toBe(false);
    expect(attention({ status: "Up 2 days (healthy)" })).toBe(false);
  });
});

describe("filters and groups", () => {
  const items = [
    container("immich-server", { project: "a-nas-immich" }),
    container("redis", { project: "a-nas-immich", state: "exited", status: "Exited (1) 1 hour ago" }),
    container("watchtower"),
    container("jellyfin", { project: "media", state: "paused", status: "Up 1 hour (Paused)" }),
  ];

  it("counts paused and restarting containers as running", () => {
    expect(items.filter((item) => matchesFilter(item, "running")).map((item) => item.name)).toEqual([
      "immich-server",
      "watchtower",
      "jellyfin",
    ]);
    expect(items.filter((item) => matchesFilter(item, "stopped")).map((item) => item.name)).toEqual(["redis"]);
    expect(items.filter((item) => matchesFilter(item, "attention")).map((item) => item.name)).toEqual(["redis"]);
  });

  it("names App Center projects after their app and lists standalone containers last", () => {
    const groups = groupContainers(items, apps);
    expect(groups.map((group) => group.title)).toEqual(["Immich", "media", "独立容器"]);
    expect(groups[0]).toMatchObject({ key: "a-nas-immich", app: immich });
    expect(groups[0].containers.map((item) => item.name)).toEqual(["immich-server", "redis"]);
    expect(appProject("immich")).toBe("a-nas-immich");
  });

  it("searches names, images and app names", () => {
    expect(items.filter((item) => matchesQuery(item, "immich redis", apps)).map((item) => item.name)).toEqual([
      "redis",
    ]);
    expect(items.filter((item) => matchesQuery(item, "IMMICH", apps))).toHaveLength(2);
  });
});

describe("images", () => {
  const image = (tags: string[], id = "sha256:abc"): ContainerImage => ({
    id,
    tags,
    sizeBytes: 1,
    createdAt: "2026-10-01T00:00:00Z",
  });

  it("matches containers by image ID when the agent reports one", () => {
    const items = [
      container("a", { image: "nginx", imageId: "sha256:abc" }),
      container("b", { image: "nginx", imageId: "sha256:old" }),
    ];
    expect(imageUsers(image(["nginx:latest"]), items).map((item) => item.name)).toEqual(["a"]);
  });

  it("falls back to normalised references from older agents", () => {
    const items = [
      container("a", { image: "docker.io/library/nginx@sha256:0123" }),
      container("b", { image: "valkey/valkey:9" }),
      container("c", { image: "sha256:abc" }),
    ];
    expect(imageUsers(image(["nginx:latest"]), items).map((item) => item.name)).toEqual(["a", "c"]);
  });

  it("splits off the registry and the tag, not at a registry port", () => {
    expect(splitReference("ghcr.io/immich-app/immich-server:v2.7.2")).toEqual({
      registry: "ghcr.io",
      repository: "immich-app/immich-server",
      tag: "v2.7.2",
    });
    expect(splitReference("registry.local:5000/tools/app")).toEqual({
      registry: "registry.local:5000",
      repository: "tools/app",
      tag: "latest",
    });
    expect(splitReference("valkey/valkey:9@sha256:3eeb")).toEqual({
      registry: "",
      repository: "valkey/valkey",
      tag: "9",
    });
  });
});

describe("webPorts", () => {
  const location = new URL("http://nas.local:8080/desktop?x=1#y") as unknown as Location;

  it("opens published TCP ports on the desktop's host, an app's own port with its scheme and path", () => {
    const item = container("immich-server", {
      ports: [
        { hostPort: 2283, containerPort: 2283, protocol: "tcp" },
        { hostIp: "::", hostPort: 2283, containerPort: 2283, protocol: "tcp" },
        { hostPort: 9090, containerPort: 9090, protocol: "tcp" },
        { hostPort: 1900, containerPort: 1900, protocol: "udp" },
        { containerPort: 3001, protocol: "tcp" },
      ],
    });
    expect(webPorts(item, { ...immich, scheme: "https", path: "/photos" }, location).map((port) => port.url)).toEqual([
      "https://nas.local:2283/photos",
      "http://nas.local:9090/",
    ]);
  });
});
