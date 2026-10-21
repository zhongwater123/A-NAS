import { CheckCircle2, CircleAlert, X } from "lucide-react";
import { createContext, ReactNode, useCallback, useContext, useEffect, useRef, useState } from "react";

import type { Speaker, VoiceStatus } from "./voiceApi";
import { initial, voiceColor } from "./format";

export interface VoiceContextValue {
  userID: string;
  admin: boolean;
  status?: VoiceStatus;
  toast: (message: string, tone?: "ok" | "error") => void;
  // changed asks the list to load again, after an edit that shows there.
  changed: () => void;
  openSettings: () => void;
}

export const VoiceContext = createContext<VoiceContextValue | undefined>(undefined);

export function useVoice() {
  const value = useContext(VoiceContext);
  if (!value) throw new Error("useVoice outside VoicePanel");
  return value;
}

interface Toast { id: number; message: string; tone: "ok" | "error" }

// useToasts keeps the window's short notices, each for a few seconds.
export function useToasts() {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);
  const timers = useRef(new Map<number, number>());
  useEffect(() => () => { for (const timer of timers.current.values()) window.clearTimeout(timer); }, []);
  const dismiss = useCallback((id: number) => {
    setToasts((current) => current.filter((toast) => toast.id !== id));
    window.clearTimeout(timers.current.get(id));
    timers.current.delete(id);
  }, []);
  const toast = useCallback((message: string, tone: "ok" | "error" = "ok") => {
    const id = ++next.current;
    setToasts((current) => [...current.slice(-2), { id, message, tone }]);
    timers.current.set(id, window.setTimeout(() => dismiss(id), tone === "error" ? 6000 : 3200));
  }, [dismiss]);
  const view = (
    <div className="vo-toasts" aria-live="polite">
      {toasts.map((item) => (
        <div key={item.id} className={`vo-toast ${item.tone}`} role={item.tone === "error" ? "alert" : "status"}>
          {item.tone === "error" ? <CircleAlert /> : <CheckCircle2 />}
          <span>{item.message}</span>
          <button type="button" aria-label="关闭提示" onClick={() => dismiss(item.id)}><X /></button>
        </div>
      ))}
    </div>
  );
  return { toast, view };
}

// VoiceMark is a speaker's round mark: their colour, with the first
// character of their name or their number.
export function VoiceMark({ speaker, size = 30 }: { speaker: Pick<Speaker, "label" | "personId" | "number">; size?: number }) {
  return (
    <span className={`vo-mark${speaker.personId ? " named" : ""}`} style={{ width: size, height: size, fontSize: size * 0.42, background: voiceColor(speaker.number) }} aria-hidden="true">
      {initial(speaker)}
    </span>
  );
}

export function Spinner({ small }: { small?: boolean }) {
  return <span className={`vo-spinner${small ? " small" : ""}`} aria-hidden="true" />;
}

// Waveform draws a few bars that move while work goes on.
export function Waveform({ bars = 9, active = true }: { bars?: number; active?: boolean }) {
  return (
    <span className={`vo-wave${active ? " active" : ""}`} aria-hidden="true">
      {Array.from({ length: bars }, (_, index) => <i key={index} style={{ animationDelay: `${(index % 5) * -0.18}s`, height: `${30 + ((index * 37) % 70)}%` }} />)}
    </span>
  );
}

export function messageOf(caught: unknown, fallback = "操作失败，请稍后再试") {
  if (caught instanceof Error && caught.message) return translateError(caught.message, fallback);
  return fallback;
}

// The API answers in English; the few messages members can meet are shown
// in their words.
const errorTexts: Record<string, string> = {
  "the file is already in 语音处理": "这段录音已经导入过了",
  "the file holds no readable audio": "这个文件里没有能识别的声音",
  "the recording lasts longer than 5 hours": "录音超过 5 小时，请分段后再导入",
  "another person has this name": "已经有人叫这个名字了",
  "the recording has no transcript yet": "录音还在处理，稍后再试",
  "a part of voice processing is unavailable": "语音处理的部分组件不可用",
  "the data volume is unavailable": "数据卷暂时不可用",
  "resource not found": "找不到这项内容，可能已被删除",
};

export function translateError(message: string, fallback: string) {
  return errorTexts[message] ?? (/[一-鿿]/.test(message) ? message : fallback);
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="vo-field">
      <span className="vo-field-label">{label}</span>
      {children}
      {hint && <small>{hint}</small>}
    </label>
  );
}
