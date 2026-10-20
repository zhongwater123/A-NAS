import { Clapperboard, Monitor, Moon } from "lucide-react";
import { useEffect, useState } from "react";

import { readLocalDisplay, updateLocalDisplay } from "../api";
import { localConsoleScreenSaverIdleMs } from "../LocalConsoleScreenSaver";
import { Banner, Group, IconTile, PageHeader, messageOf } from "./parts";
import "./personalization.css";

const saverDelayMinutes = localConsoleScreenSaverIdleMs / 60_000;

const sleepChoices: Array<{ minutes: number; label: string }> = [
  { minutes: 10, label: "10 分钟" },
  { minutes: 30, label: "30 分钟" },
  { minutes: 60, label: "1 小时" },
  { minutes: 0, label: "从不" },
];

function minutesText(minutes: number): string {
  return minutes % 60 === 0 ? `${minutes / 60} 小时` : `${minutes} 分钟`;
}

// PersonalizationSection is Settings → 个性化. The screen attached to the NAS
// is shared by the household, so only administrators change it.
export function PersonalizationSection({ isAdmin }: { isAdmin: boolean }) {
  const [sleepAfter, setSleepAfter] = useState<number>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    readLocalDisplay()
      .then((display) => { if (active) setSleepAfter(display.sleepAfterMinutes); })
      .catch((caught: unknown) => { if (active) setError(messageOf(caught)); });
    return () => { active = false; };
  }, []);
  const choose = async (minutes: number) => {
    if (sleepAfter === undefined || minutes === sleepAfter) return;
    const previous = sleepAfter;
    setSleepAfter(minutes); setSaving(true); setError("");
    try { setSleepAfter((await updateLocalDisplay(minutes)).sleepAfterMinutes); }
    catch (caught) { setSleepAfter(previous); setError(messageOf(caught)); }
    finally { setSaving(false); }
  };
  return (
    <>
      <PageHeader title="个性化" subtitle="桌面外观与本机屏幕" />
      {error && <Banner tone="error" onClose={() => setError("")}>{error}</Banner>}
      <Group title="本机屏幕" footnote="本机屏幕是直接接在这台 NAS 上的显示器，用手机或电脑浏览器访问时不会息屏。黑屏时视频停止播放，显示器背光仍按它自己的亮度点亮。">
        <div className="set-row pz-row">
          <IconTile tone="sleep" icon={Moon} />
          <div className="set-row-text">
            <span>息屏</span>
            <small>{isAdmin ? "屏保播放多久后让屏幕变黑" : "由管理员设置"}</small>
          </div>
          <div className="pz-segmented" role="radiogroup" aria-label="息屏时间" aria-busy={saving || undefined}>
            {sleepChoices.map(({ minutes, label }) => (
              <button key={minutes} type="button" role="radio" aria-checked={sleepAfter === minutes}
                disabled={!isAdmin || saving || sleepAfter === undefined} onClick={() => void choose(minutes)}>
                {label}
              </button>
            ))}
          </div>
        </div>
        {sleepAfter !== undefined && <SleepTimeline sleepAfter={sleepAfter} />}
      </Group>
    </>
  );
}

// SleepTimeline shows what the screen does after the last input; stage widths
// are fixed so short and long choices stay readable.
function SleepTimeline({ sleepAfter }: { sleepAfter: number }) {
  const sleeps = sleepAfter > 0;
  const blackAt = saverDelayMinutes + sleepAfter;
  return (
    <figure className={`pz-timeline ${sleeps ? "" : "never"}`}>
      <div className="pz-track" aria-hidden="true">
        <span className="pz-stage desktop"><Monitor />桌面</span>
        <span className="pz-stage saver"><Clapperboard />屏保</span>
        {sleeps && <span className="pz-stage asleep"><Moon />黑屏</span>}
      </div>
      <div className="pz-ticks" aria-hidden="true">
        <span>最后一次操作</span>
        <span>{saverDelayMinutes} 分钟</span>
        {sleeps && <span>{minutesText(blackAt)}</span>}
      </div>
      <figcaption>
        {sleeps
          ? `登录页没有屏保，${minutesText(blackAt)}后直接变黑。按任意键、移动鼠标或触摸屏幕即可唤醒。`
          : "屏保会一直播放，登录页一直亮着。"}
      </figcaption>
    </figure>
  );
}
