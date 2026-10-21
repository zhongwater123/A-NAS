import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api";
import { exportText, paragraphs } from "./format";
import { activeSeq } from "./Transcript";
import type { Recording, RecordingDetail, Speaker, VoiceStatus } from "./voiceApi";
import { VoicePanel } from "./VoicePanel";

const status: VoiceStatus = {
  recognizer: { available: true, name: "豆包语音" },
  textModel: { available: true, name: "deepseek-flash" },
  voiceprints: { available: true, name: "campplus" },
  decoder: { available: true },
  templates: [
    { id: "general", name: "通用摘要", description: "提炼要点与待办", sections: [{ key: "points", title: "要点" }], actions: true },
    { id: "meeting", name: "会议纪要", description: "议题、结论与待办", sections: [{ key: "topics", title: "议题与讨论" }], actions: true },
  ],
  languages: [{ code: "en", name: "英语" }, { code: "ja", name: "日语" }, { code: "ko", name: "韩语" }],
  spokenLanguages: [{ code: "", name: "自动" }],
};

const base: Recording = {
  id: "recording:1", fileId: "file:1", spaceId: "space:me", name: "周会.m4a", title: "十月周会", titleEdited: false, sizeBytes: 1000,
  durationMs: 30_000, language: "", template: "meeting", translateTo: "", hotwords: [], status: "ready", progress: 1, transcode: false,
  speakers: 2, people: [], preview: "讨论了装修安排", textRevision: 1, namesRevision: 1, voiceprints: true,
  createdAt: "2026-10-21T01:00:00Z", updatedAt: "2026-10-21T01:00:00Z",
};

const speakers: Speaker[] = [
  { key: "s1", number: 1, label: "发言人 1", speechMs: 8000, voiceprint: true },
  { key: "s2", number: 2, label: "发言人 2", speechMs: 4000, voiceprint: true, suggestion: { personId: "person:li", name: "李四", score: 0.55 } },
];

function detailOf(overrides: Partial<RecordingDetail> = {}): RecordingDetail {
  return {
    ...base,
    utterances: [
      { seq: 1, startMs: 0, endMs: 4000, speaker: "s1", text: "我们开始开会。", edited: false },
      { seq: 2, startMs: 4500, endMs: 8000, speaker: "s1", text: "先说预算。", edited: false },
      { seq: 3, startMs: 9000, endMs: 13000, speaker: "s2", text: "预算多了三千。", edited: false },
    ],
    speakerList: speakers,
    translations: [],
    summaries: [{
      template: "meeting", status: "ready", textRevision: 1, namesRevision: 1, model: "deepseek-flash", updatedAt: "2026-10-21T01:01:00Z",
      content: { title: "装修周会", overview: "大家讨论了预算。", sections: [{ key: "topics", title: "议题与讨论", items: ["00:09 预算超出三千元"] }], actions: [{ task: "交押金", owner: "张三", due: "明天" }], keywords: ["预算"] },
    }],
    ...overrides,
  };
}

function json(body: unknown, statusCode = 200) {
  return new Response(JSON.stringify(body), { status: statusCode, headers: { "Content-Type": "application/json" } });
}

function serve(detail = detailOf(), recordings: Recording[] = [base]) {
  let current = detail;
  const calls: Array<{ url: string; method: string; body?: unknown }> = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url, method, body });
    if (url === "/api/v1/voice/status") return json(status);
    if (url === "/api/v1/voice/recordings" && method === "GET") return json({ recordings });
    if (url === "/api/v1/voice/people") return json({ people: [{ id: "person:li", name: "李四", samples: 1, recordings: 1, speechMs: 1000, createdAt: "", updatedAt: "" }] });
    if (url === "/api/v1/voice/recordings/recording%3A1" && method === "GET") return json(current);
    if (url === "/api/v1/voice/recordings/recording%3A1/speakers/s1" && method === "PUT") {
      current = { ...current, speakerList: current.speakerList.map((speaker) => speaker.key === "s1" ? { ...speaker, label: body.name, personId: "person:new", assigned: "user" } : speaker) };
      return json(current);
    }
    if (url === "/api/v1/voice/settings/recognizer" && method === "GET") return json({ provider: "doubao", configured: false, regions: ["cn-beijing", "cn-shanghai"] });
    if (url === "/api/v1/voice/settings/recognizer/test" && method === "POST") return json({ ok: true, queued: true });
    if (url === "/api/v1/voice/settings/recognizer" && method === "PUT") {
      return json({ provider: "doubao", configured: true, mode: "apiKey", secretHint: "••••1234", regions: ["cn-beijing", "cn-shanghai"], storage: { region: body.storage.region, bucket: body.storage.bucket, accessKeyHint: "••••5678", secretHint: "••••wxyz" } });
    }
    if (url === "/api/v1/voice/recordings/recording%3A1/translations" && method === "POST") {
      current = { ...current, translations: [{ language: body.language, status: "queued", revision: 0, progress: 0, lines: [], updatedAt: "" }] };
      return json(current, 202);
    }
    return json({ error: { code: "not_found", message: "resource not found" } }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

beforeEach(() => {
  setSession({ csrfToken: "csrf", expiresAt: "2026-10-22T00:00:00Z", user: { id: "user:u1", username: "u1", role: "member", status: "active", createdAt: "", mustChangePassword: false } } as never);
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(() => Promise.resolve());
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => undefined);
});

afterEach(() => {
  // Unmount while play and pause are still stubbed: jsdom has no media.
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("VoicePanel", () => {
  it("opens the first recording with its speakers, transcript and summary", async () => {
    serve();
    render(<VoicePanel userId="user:u1" isAdmin={false} onOpenFiles={() => undefined} />);
    expect(await screen.findByRole("heading", { name: "十月周会" }, { timeout: 5000 })).toBeTruthy();
    expect(screen.getByText("我们开始开会。")).toBeTruthy();
    // Two sentences of one speaker are one turn.
    expect(screen.getAllByText("发言人 1").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("可能是 李四？")).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: /总结/ }));
    expect(await screen.findByText("装修周会")).toBeTruthy();
    expect(screen.getByText("交押金")).toBeTruthy();
    expect(screen.getByRole("button", { name: "00:09" })).toBeTruthy();
  });

  it("names a speaker from the speaker strip", async () => {
    const calls = serve();
    render(<VoicePanel userId="user:u1" isAdmin={false} onOpenFiles={() => undefined} />);
    await screen.findByRole("heading", { name: "十月周会" }, { timeout: 5000 });
    fireEvent.click(screen.getAllByRole("button", { name: "发言人 1，点击命名" })[0]);
    const input = await screen.findByPlaceholderText("输入名字");
    fireEvent.change(input, { target: { value: "张三" } });
    fireEvent.click(screen.getByRole("button", { name: "确定" }));
    await waitFor(() => expect(calls.some((call) => call.method === "PUT" && (call.body as { name?: string })?.name === "张三")).toBe(true));
    expect((await screen.findAllByText("张三")).length).toBeGreaterThan(0);
  });

  it("asks for a translation and shows it on its way", async () => {
    const calls = serve();
    render(<VoicePanel userId="user:u1" isAdmin={false} onOpenFiles={() => undefined} />);
    await screen.findByRole("heading", { name: "十月周会" }, { timeout: 5000 });
    fireEvent.click(screen.getByRole("tab", { name: /翻译/ }));
    fireEvent.click(await screen.findByRole("button", { name: "日语" }));
    await waitFor(() => expect(calls.some((call) => call.method === "POST" && call.url.endsWith("/translations"))).toBe(true));
    expect(await screen.findByText("正在译成日语")).toBeTruthy();
  });

  it("explains a recording that waits for the recognition service", async () => {
    const waiting = { ...base, status: "waiting" as const, waiting: "recognizer" as const, speakers: 0 };
    serve(detailOf({ ...waiting, utterances: [], speakerList: [], summaries: [] }), [waiting]);
    render(<VoicePanel userId="user:u1" isAdmin={false} onOpenFiles={() => undefined} />);
    expect(await screen.findByRole("heading", { name: "等待配置豆包语音" }, { timeout: 5000 })).toBeTruthy();
    const list = screen.getByRole("listbox", { name: "录音列表" });
    expect(within(list).getByText("等待管理员配置豆包语音")).toBeTruthy();
  });

  it("lets an administrator test and save Doubao's key with the bucket that relays recordings", async () => {
    const calls = serve();
    render(<VoicePanel userId="user:u1" isAdmin onOpenFiles={() => undefined} />);
    await screen.findByRole("heading", { name: "十月周会" }, { timeout: 5000 });
    fireEvent.click(screen.getByRole("button", { name: /服务与设置/ }));
    fireEvent.change(await screen.findByPlaceholderText("粘贴 API Key"), { target: { value: "doubao-key-1234" } });
    const save = screen.getByRole("button", { name: "测试并保存" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("地域"), { target: { value: "cn-shanghai" } });
    fireEvent.change(screen.getByPlaceholderText("例如 family-voice"), { target: { value: "family-voice" } });
    fireEvent.change(screen.getByPlaceholderText("AKLT…"), { target: { value: "AKLT5678" } });
    fireEvent.change(screen.getByPlaceholderText("粘贴 Secret Access Key"), { target: { value: "secret-wxyz" } });
    fireEvent.click(save);
    expect(await screen.findByText(/仍在排队/)).toBeTruthy();
    await waitFor(() => expect(calls.some((call) => call.method === "PUT")).toBe(true));
    const storage = { region: "cn-shanghai", bucket: "family-voice", accessKeyId: "AKLT5678", secretAccessKey: "secret-wxyz" };
    expect(calls.find((call) => call.url.endsWith("/settings/recognizer/test"))?.body).toEqual({ mode: "apiKey", apiKey: "doubao-key-1234", storage });
    expect(calls.find((call) => call.method === "PUT")?.body).toEqual({ mode: "apiKey", apiKey: "doubao-key-1234", storage });
    expect(await screen.findByText(/上海 · cn-shanghai \/ family-voice/)).toBeTruthy();
  });

  it("welcomes a member without recordings", async () => {
    serve(detailOf(), []);
    render(<VoicePanel userId="user:u1" isAdmin={false} onOpenFiles={() => undefined} />);
    expect(await screen.findByRole("button", { name: /导入第一段录音/ }, { timeout: 5000 })).toBeTruthy();
  });
});

describe("voice helpers", () => {
  it("groups sentences into turns and finds the one playing", () => {
    const detail = detailOf();
    expect(paragraphs(detail.utterances).map((turn) => turn.utterances.length)).toEqual([2, 1]);
    expect(activeSeq(detail.utterances, 5000)).toBe(2);
    expect(activeSeq(detail.utterances, 8300)).toBe(2);
    expect(activeSeq(detail.utterances, 8600)).toBeUndefined();
  });

  it("exports subtitles and the summary", () => {
    const detail = detailOf();
    const srt = exportText(detail, "transcript-srt").text;
    expect(srt).toContain("00:00:09,000 --> 00:00:13,000");
    expect(srt).toContain("发言人 2：预算多了三千。");
    const summary = exportText(detail, "summary-md", { summary: detail.summaries[0], template: status.templates[1] }).text;
    expect(summary).toContain("# 装修周会");
    expect(summary).toContain("- [ ] 交押金（张三） · 明天");
  });
});
