import { Check, CircleAlert, Clock3, KeyRound, LogIn, RotateCcw } from "lucide-react";
import { useState } from "react";

import { messageOf, Spinner, useVoice, Waveform } from "./context";
import { duration } from "./format";
import { type RecordingDetail, retryRecording } from "./voiceApi";

const steps = [
  { stage: "preparing", title: "准备音频", detail: "在 NAS 上把录音转成识别服务需要的格式" },
  { stage: "recognizing", title: "识别文字", detail: "豆包语音转写说话内容，分开不同的人" },
  { stage: "identifying", title: "识别发言人", detail: "在本机计算声纹，认出你命名过的人" },
] as const;

const failures: Record<string, string> = {
  file_missing: "找不到录音文件，它可能被移动、改名后又移出了你能访问的位置，或已删除。",
  not_audio: "这个文件里没有能识别的声音。",
  too_long: "录音超过 5 小时，请分段后再导入。",
  recognizer_rejected: "豆包语音拒绝了密钥，请管理员在“服务与设置”中检查后重试。",
  recognizer_failed: "豆包语音没能识别这段录音。",
  decoder_unavailable: "NAS 上的音频转换组件暂时不可用。",
  internal: "处理时出了意外的错误。",
};

// ProcessingCard shows a recording on its way to a transcript: which step it
// is at, what it waits for, or why it stopped.
export function ProcessingCard({ detail, onChange }: { detail: RecordingDetail; onChange: () => void }) {
  const voice = useVoice();
  const [retrying, setRetrying] = useState(false);
  const retry = async () => {
    setRetrying(true);
    try {
      await retryRecording(detail.id);
      onChange();
      voice.changed();
    } catch (caught) {
      voice.toast(messageOf(caught, "重试失败"), "error");
    } finally {
      setRetrying(false);
    }
  };
  if (detail.status === "failed") {
    return (
      <section className="vo-state failed">
        <span className="vo-state-icon"><CircleAlert /></span>
        <h3>没能完成这段录音</h3>
        <p>{failures[detail.error?.code ?? ""] ?? failures.internal}</p>
        {detail.error?.message && <details><summary>详细信息</summary><code>{detail.error.message}</code></details>}
        <div className="vo-state-actions">
          <button type="button" className="vo-button primary" disabled={retrying} onClick={() => void retry()}><RotateCcw />重新处理</button>
          {detail.error?.code === "recognizer_rejected" && <button type="button" className="vo-button" onClick={voice.openSettings}><KeyRound />服务与设置</button>}
        </div>
      </section>
    );
  }
  if (detail.status === "waiting") {
    const reason = detail.waiting;
    return (
      <section className="vo-state waiting">
        <span className="vo-state-icon">{reason === "recognizer" ? <KeyRound /> : reason === "sign_in" ? <LogIn /> : <Clock3 />}</span>
        <h3>{reason === "recognizer" ? "等待配置豆包语音" : reason === "sign_in" ? "等待你登录" : "识别服务暂时繁忙"}</h3>
        <p>
          {reason === "recognizer"
            ? voice.admin ? "这台 NAS 还没有豆包语音的密钥。填好后，排队的录音会自动开始。" : "这台 NAS 还没有配置豆包语音的密钥，请管理员在“语音处理 → 服务与设置”中填写。填好后会自动开始。"
            : reason === "sign_in" ? "录音只能以你的身份读取。NAS 重启后，你登录一次就会继续。" : "已经重试过几次，稍后会自动再试；也可以现在重试。"}
        </p>
        <div className="vo-state-actions">
          {reason === "recognizer" && voice.admin && <button type="button" className="vo-button primary" onClick={voice.openSettings}><KeyRound />填写密钥</button>}
          {reason === "busy" && <button type="button" className="vo-button" disabled={retrying} onClick={() => void retry()}><RotateCcw />现在重试</button>}
        </div>
      </section>
    );
  }
  const current = detail.status === "queued" ? -1 : steps.findIndex((step) => step.stage === detail.stage);
  return (
    <section className="vo-state working" aria-live="polite">
      <div className="vo-state-wave"><Waveform bars={21} /></div>
      <h3>{detail.status === "queued" ? "排队等待处理" : `正在${steps[Math.max(0, current)].title}`}</h3>
      <p>{duration(detail.durationMs)}的录音，通常需要{detail.durationMs > 1_800_000 ? "几分钟" : "一两分钟"}。可以关掉窗口，处理在后台继续。</p>
      <ol className="vo-steps">
        {steps.map((step, index) => {
          const state = index < current ? "done" : index === current ? "active" : "todo";
          return (
            <li key={step.stage} className={state}>
              <span className="vo-step-mark">{state === "done" ? <Check /> : state === "active" ? <Spinner small /> : index + 1}</span>
              <span><strong>{step.title}</strong><small>{step.detail}</small></span>
            </li>
          );
        })}
      </ol>
      <div className="vo-progress"><i style={{ width: `${Math.max(4, detail.progress * 100)}%` }} /></div>
    </section>
  );
}
