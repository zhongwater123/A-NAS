import { Clapperboard, ImagePlus, Monitor, Moon, RotateCcw } from "lucide-react";
import { useEffect, useState } from "react";

import type { LocalDisplay, Wallpaper, WallpaperStyle } from "../api.generated";
import { readLocalDisplay, resetWallpaper, updateLocalDisplay, updateWallpaperStyle } from "../desktop/api";
import { localConsoleScreenSaverIdleMs } from "../desktop/LocalConsoleScreenSaver";
import { publishWallpaper, useWallpaper, wallpaperPicture } from "../desktop/wallpaper";
import { DesktopPreview } from "./DesktopPreview";
import { Banner, Group, messageOf, PageHeader, Segmented } from "./parts";
import styles from "./settings.module.css";
import { WallpaperDialog } from "./WallpaperDialog";

const saverDelayMinutes = localConsoleScreenSaverIdleMs / 60_000;

type SleepAfter = LocalDisplay["sleepAfterMinutes"];

const sleepChoices: Array<{ minutes: SleepAfter; label: string }> = [
  { minutes: 10, label: "10 分钟" },
  { minutes: 30, label: "30 分钟" },
  { minutes: 60, label: "1 小时" },
  { minutes: 0, label: "从不" },
];

const styleChoices: Array<{ style: WallpaperStyle; label: string }> = [
  // Keeps the ratio and covers the desktop, cropping what does not fit.
  { style: "fill", label: "填充" },
  // Shows the whole picture; the rest of the desktop is dark blue.
  { style: "fit", label: "适应" },
  // Stretches to the desktop's width and height; the ratio may change.
  { style: "stretch", label: "拉伸" },
  // The picture at its own size, in the middle.
  { style: "center", label: "居中" },
  // The picture at its own size, repeated across the desktop.
  { style: "tile", label: "平铺" },
];

function minutesText(minutes: number): string {
  return minutes % 60 === 0 ? `${minutes / 60} 小时` : `${minutes} 分钟`;
}

// PersonalizationSection is Settings → 个性化, shown to administrators only:
// the wallpaper and the screen attached to the NAS are the same for the whole
// household.
export function PersonalizationSection() {
  const [error, setError] = useState("");
  return (
    <>
      <PageHeader title="个性化" />
      {error && (
        <Banner tone="error" onClose={() => setError("")}>
          {error}
        </Banner>
      )}
      <WallpaperGroup onError={setError} />
      <LocalScreenGroup onError={setError} />
    </>
  );
}

function WallpaperGroup({ onError }: { onError: (message: string) => void }) {
  const wallpaper = useWallpaper();
  const [busy, setBusy] = useState(false);
  const [choosing, setChoosing] = useState(false);
  const [confirmingReset, setConfirmingReset] = useState(false);
  // A change shows at once; a failed one puts the previous wallpaper back.
  const run = async (action: () => Promise<Wallpaper>, previous?: Wallpaper) => {
    setBusy(true);
    onError("");
    try {
      publishWallpaper(await action());
    } catch (caught) {
      if (previous) publishWallpaper(previous);
      onError(messageOf(caught));
    } finally {
      setBusy(false);
    }
  };
  const chooseStyle = (style: WallpaperStyle) => {
    if (!wallpaper || style === wallpaper.style) return;
    publishWallpaper({ ...wallpaper, style });
    void run(() => updateWallpaperStyle(style), wallpaper);
  };
  const custom = wallpaper?.image;
  const style = wallpaper?.style ?? "fill";
  return (
    <>
      {/* One wallpaper for every account's desktop and the device's own screen.
          A picture is scaled to at most 3840 px on its long side before upload
          and carries no location or other metadata. */}
      <Group title="桌面壁纸" anchor="wallpaper">
        <div className={styles.pzWallpaper}>
          <DesktopPreview picture={wallpaper && wallpaperPicture(wallpaper)} style={style} label="当前桌面壁纸的预览" />
        </div>
        {confirmingReset ? (
          <div className={`${styles.setRow} ${styles.pzRow}`} role="group" aria-label="确认恢复默认壁纸">
            <div className={styles.setRowText}>
              <span>所有桌面会换回默认壁纸，样式改回“填充”。</span>
            </div>
            <div className={styles.setRowEnd}>
              <button className={styles.setBtn} onClick={() => setConfirmingReset(false)} disabled={busy}>
                取消
              </button>
              <button
                className={`${styles.setBtn} ${styles.danger}`}
                onClick={() => {
                  setConfirmingReset(false);
                  void run(resetWallpaper);
                }}
                disabled={busy}
              >
                恢复默认
              </button>
            </div>
          </div>
        ) : (
          <div className={`${styles.setRow} ${styles.pzRow}`}>
            <div className={styles.setRowText}>
              <span>{wallpaper === undefined ? "正在读取…" : custom ? "自定义图片" : "默认壁纸"}</span>
              {custom && (
                <small>
                  {custom.width} × {custom.height} 像素
                </small>
              )}
            </div>
            <div className={styles.setRowEnd}>
              {custom && (
                <button className={styles.setBtn} onClick={() => setConfirmingReset(true)} disabled={busy}>
                  <RotateCcw />
                  恢复默认
                </button>
              )}
              <button className={styles.setBtn} onClick={() => setChoosing(true)} disabled={busy || !wallpaper}>
                <ImagePlus />
                更换图片…
              </button>
            </div>
          </div>
        )}
        <div className={`${styles.setRow} ${styles.pzRow}`} data-setting="wallpaper-style">
          <div className={styles.setRowText}>
            <span>样式</span>
          </div>
          <Segmented
            label="壁纸样式"
            value={wallpaper?.style}
            choices={styleChoices.map((choice) => ({ value: choice.style, label: choice.label }))}
            busy={busy}
            disabled={busy || !wallpaper}
            onChange={chooseStyle}
          />
        </div>
      </Group>
      {choosing && (
        <WallpaperDialog
          style={style}
          onClose={() => setChoosing(false)}
          onSaved={(next) => {
            publishWallpaper(next);
            setChoosing(false);
          }}
        />
      )}
    </>
  );
}

function LocalScreenGroup({ onError }: { onError: (message: string) => void }) {
  const [sleepAfter, setSleepAfter] = useState<SleepAfter>();
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    let active = true;
    readLocalDisplay()
      .then((display) => {
        if (active) setSleepAfter(display.sleepAfterMinutes);
      })
      .catch((caught: unknown) => {
        if (active) onError(messageOf(caught));
      });
    return () => {
      active = false;
    };
  }, [onError]);
  const choose = async (minutes: SleepAfter) => {
    if (sleepAfter === undefined || minutes === sleepAfter) return;
    const previous = sleepAfter;
    setSleepAfter(minutes);
    setSaving(true);
    onError("");
    try {
      setSleepAfter((await updateLocalDisplay(minutes)).sleepAfterMinutes);
    } catch (caught) {
      setSleepAfter(previous);
      onError(messageOf(caught));
    } finally {
      setSaving(false);
    }
  };
  return (
    // 本机屏幕 is the display wired to the NAS; a phone or computer browser never
    // sleeps. Black stops the video, but the panel's backlight stays as it is
    // (docs/specs/personalization.md).
    <Group title="本机屏幕" anchor="sleep">
      <div className={`${styles.setRow} ${styles.pzRow}`}>
        <div className={styles.setRowText}>
          <span>息屏</span>
        </div>
        <Segmented
          label="息屏时间"
          value={sleepAfter}
          choices={sleepChoices.map(({ minutes, label }) => ({ value: minutes, label }))}
          busy={saving}
          disabled={saving || sleepAfter === undefined}
          onChange={(minutes) => void choose(minutes)}
        />
      </div>
      {sleepAfter !== undefined && <SleepTimeline sleepAfter={sleepAfter} />}
    </Group>
  );
}

// SleepTimeline shows what the screen does after the last input; stage widths
// are fixed so short and long choices stay readable.
function SleepTimeline({ sleepAfter }: { sleepAfter: number }) {
  const sleeps = sleepAfter > 0;
  const blackAt = saverDelayMinutes + sleepAfter;
  return (
    // The sign-in page has no screensaver: it goes black after the same time
    // without input. Any key, pointer move or touch wakes the screen.
    <figure
      className={`${styles.pzTimeline} ${sleeps ? "" : styles.never}`}
      aria-label={
        sleeps
          ? `${saverDelayMinutes} 分钟后播放屏保，${minutesText(blackAt)}后黑屏`
          : `${saverDelayMinutes} 分钟后播放屏保，不黑屏`
      }
    >
      <div className={styles.pzTrack} aria-hidden="true">
        <span className={`${styles.pzStage} ${styles.desktop}`}>
          <Monitor />
          桌面
        </span>
        <span className={`${styles.pzStage} ${styles.saver}`}>
          <Clapperboard />
          屏保
        </span>
        {sleeps && (
          <span className={`${styles.pzStage} ${styles.asleep}`}>
            <Moon />
            黑屏
          </span>
        )}
      </div>
      <div className={styles.pzTicks} aria-hidden="true">
        <span>最后一次操作</span>
        <span>{saverDelayMinutes} 分钟</span>
        {sleeps && <span>{minutesText(blackAt)}</span>}
      </div>
    </figure>
  );
}
