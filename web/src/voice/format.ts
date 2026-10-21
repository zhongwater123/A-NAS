import type { RecordingDetail, Speaker, Summary, Template, Translation, Utterance } from "./voiceApi";

// clock is m:ss, or h:mm:ss from an hour.
export function clock(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor(total / 60) % 60;
  const seconds = String(total % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${seconds}` : `${minutes}:${seconds}`;
}

// duration reads like "32 分钟" or "1 小时 5 分".
export function duration(ms: number): string {
  const minutes = Math.round(ms / 60000);
  if (ms < 60000) return `${Math.max(1, Math.round(ms / 1000))} 秒`;
  if (minutes < 60) return `${minutes} 分钟`;
  const rest = minutes % 60;
  return rest ? `${Math.floor(minutes / 60)} 小时 ${rest} 分` : `${minutes / 60} 小时`;
}

const dayFormat = new Intl.DateTimeFormat("zh-CN", { month: "numeric", day: "numeric" });
const timeFormat = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });

// when is 今天 14:05, 昨天 09:30, 10月18日 or a full date in another year.
export function when(iso: string, now = new Date()): string {
  const date = new Date(iso);
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const day = 86_400_000;
  if (date.getTime() >= start) return `今天 ${timeFormat.format(date)}`;
  if (date.getTime() >= start - day) return `昨天 ${timeFormat.format(date)}`;
  if (date.getFullYear() === now.getFullYear()) return dayFormat.format(date);
  return date.toLocaleDateString("zh-CN");
}

// Voices are told apart by colour, in the order they first speak.
export const voiceColors = ["#6d5dfc", "#f08a24", "#13a489", "#e5487b", "#2f8fe0", "#9a5bd6", "#c7a516", "#d9583a", "#3a9b4d", "#5f6f86"];
export const voiceColor = (number: number) => voiceColors[(Math.max(1, number) - 1) % voiceColors.length];

// initial is what a speaker's round mark shows: a name's first character, or
// the number of 发言人 N.
export function initial(speaker: Pick<Speaker, "label" | "personId" | "number">): string {
  if (!speaker.personId) return String(speaker.number);
  return Array.from(speaker.label.trim())[0] ?? "?";
}

// Paragraph is a speaker's turn: their consecutive sentences.
export interface Paragraph { speaker: string; startMs: number; utterances: Utterance[] }

export function paragraphs(utterances: Utterance[], gapMs = 8000): Paragraph[] {
  const result: Paragraph[] = [];
  for (const utterance of utterances) {
    const last = result.at(-1);
    const previous = last?.utterances.at(-1);
    if (last && previous && last.speaker === utterance.speaker && utterance.startMs - previous.endMs <= gapMs && last.utterances.length < 12) {
      last.utterances.push(utterance);
    } else {
      result.push({ speaker: utterance.speaker, startMs: utterance.startMs, utterances: [utterance] });
    }
  }
  return result;
}

// stale says whether a result was made from an older transcript.
export function staleTranslation(recording: RecordingDetail, translation: Translation) {
  return translation.status === "ready" && translation.revision !== recording.textRevision;
}

export function staleSummary(recording: RecordingDetail, summary: Summary) {
  return summary.status === "ready" && (summary.textRevision !== recording.textRevision || summary.namesRevision !== recording.namesRevision);
}

function srtTime(ms: number) {
  const hours = Math.floor(ms / 3_600_000);
  const minutes = Math.floor(ms / 60000) % 60;
  const seconds = Math.floor(ms / 1000) % 60;
  const millis = Math.floor(ms % 1000);
  const two = (value: number) => String(value).padStart(2, "0");
  return `${two(hours)}:${two(minutes)}:${two(seconds)},${String(millis).padStart(3, "0")}`;
}

export type ExportKind = "transcript-txt" | "transcript-srt" | "transcript-md" | "translation-txt" | "translation-srt" | "summary-md";

// exportText builds a file of the recording for download.
export function exportText(recording: RecordingDetail, kind: ExportKind, options: { translation?: Translation; summary?: Summary; template?: Template } = {}): { name: string; type: string; text: string } {
  const names = new Map(recording.speakerList.map((speaker) => [speaker.key, speaker.label]));
  const lines = options.translation ? new Map(options.translation.lines.map((line) => [line.seq, line.text])) : undefined;
  const text = (utterance: Utterance) => lines?.get(utterance.seq) ?? utterance.text;
  const stem = recording.title.replace(/[\\/:*?"<>|]/g, "_");
  switch (kind) {
    case "transcript-srt":
    case "translation-srt":
      return {
        name: `${stem}${lines ? `.${options.translation?.language}` : ""}.srt`, type: "application/x-subrip",
        text: recording.utterances.map((utterance, index) => `${index + 1}\n${srtTime(utterance.startMs)} --> ${srtTime(utterance.endMs)}\n${names.get(utterance.speaker) ?? ""}：${text(utterance)}\n`).join("\n"),
      };
    case "transcript-md":
      return {
        name: `${stem}.md`, type: "text/markdown",
        text: `# ${recording.title}\n\n${paragraphs(recording.utterances).map((paragraph) => `**${names.get(paragraph.speaker) ?? ""}** · ${clock(paragraph.startMs)}\n\n${paragraph.utterances.map((utterance) => utterance.text).join("")}\n`).join("\n")}`,
      };
    case "summary-md": {
      const content = options.summary?.content;
      const parts = [`# ${content?.title || recording.title}`, "", `> ${options.template?.name ?? ""} · ${recording.name}`, ""];
      if (content?.overview) parts.push(content.overview, "");
      for (const section of content?.sections ?? []) {
        if (!section.items.length) continue;
        parts.push(`## ${section.title}`, "", ...section.items.map((item) => `- ${item}`), "");
      }
      if (content?.actions.length) {
        parts.push("## 待办事项", "", ...content.actions.map((action) => `- [ ] ${action.task}${action.owner ? `（${action.owner}）` : ""}${action.due ? ` · ${action.due}` : ""}`), "");
      }
      if (content?.keywords.length) parts.push(`关键词：${content.keywords.join("、")}`, "");
      return { name: `${stem}.总结.md`, type: "text/markdown", text: parts.join("\n") };
    }
    default:
      return {
        name: `${stem}${lines ? `.${options.translation?.language}` : ""}.txt`, type: "text/plain",
        text: paragraphs(recording.utterances).map((paragraph) => `${names.get(paragraph.speaker) ?? ""}  ${clock(paragraph.startMs)}\n${paragraph.utterances.map(text).join(lines ? " " : "")}\n`).join("\n"),
      };
  }
}

export function download(file: { name: string; type: string; text: string }) {
  const blob = new Blob([file.text], { type: `${file.type};charset=utf-8` });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = file.name;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
