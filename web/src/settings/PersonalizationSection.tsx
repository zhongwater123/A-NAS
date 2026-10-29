import { Clapperboard, ImagePlus, Monitor, Moon, RotateCcw, Scaling } from "lucide-react";
import { useEffect, useState } from "react";

import type { LocalDisplay, Wallpaper, WallpaperStyle } from "../api.generated";
import { readLocalDisplay, resetWallpaper, updateLocalDisplay, updateWallpaperStyle } from "../desktop/api";
import { localConsoleScreenSaverIdleMs } from "../desktop/LocalConsoleScreenSaver";
import { publishWallpaper, useWallpaper, wallpaperPicture } from "../desktop/wallpaper";
import { DesktopPreview } from "./DesktopPreview";
import { Banner, Group, IconTile, messageOf, PageHeader } from "./parts";
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

const styleChoices: Array<{ style: WallpaperStyle; label: string; detail: string }> = [
  { style: "fill", label: "填充", detail: "保持比例铺满桌面，裁去多出的部分" },
  { style: "fit", label: "适应", detail: "完整显示整张图片，空白处为深蓝色" },
  { style: "stretch", label: "拉伸", detail: "拉到桌面的宽和高，比例可能改变" },
  { style: "center", label: "居中", detail: "按图片原始大小居中显示" },
  { style: "tile", label: "平铺", detail: "按图片原始大小重复铺满桌面" },
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
      <PageHeader title="个性化" subtitle="全家共用的桌面壁纸与本机屏幕" />
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
      <Group
        title="桌面壁纸"
        footnote="壁纸对所有账号的桌面生效，包括本机屏幕。图片上传前会缩小到长边不超过 3840 像素，不带拍摄位置等信息。"
      >
        <div className={styles.pzWallpaper}>
          <DesktopPreview picture={wallpaper && wallpaperPicture(wallpaper)} style={style} label="当前桌面壁纸的预览" />
          <div className={styles.pzWallpaperSide}>
            <div className={styles.pzWallpaperName}>
              <strong>{wallpaper === undefined ? "正在读取…" : custom ? "自定义图片" : "默认壁纸"}</strong>
              <small>{custom ? `${custom.width} × ${custom.height} 像素` : "A-NAS 内置的深蓝抽象壁纸"}</small>
            </div>
            {confirmingReset ? (
              <div className={styles.pzConfirm} role="group" aria-label="确认恢复默认壁纸">
                <p>所有桌面会换回默认壁纸，样式改回“填充”。</p>
                <div>
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
              <div className={styles.pzWallpaperActions}>
                <button
                  className={`${styles.setBtn} primary`}
                  onClick={() => setChoosing(true)}
                  disabled={busy || !wallpaper}
                >
                  <ImagePlus />
                  更换图片…
                </button>
                {custom && (
                  <button className={styles.setBtn} onClick={() => setConfirmingReset(true)} disabled={busy}>
                    <RotateCcw />
                    恢复默认
                  </button>
                )}
              </div>
            )}
          </div>
        </div>
        <div className={`${styles.setRow} ${styles.pzRow}`}>
          <IconTile tone="personalize" icon={Scaling} />
          <div className={styles.setRowText}>
            <span>样式</span>
            <small>{styleChoices.find((choice) => choice.style === style)?.detail}</small>
          </div>
          <div className={styles.pzSegmented} role="radiogroup" aria-label="壁纸样式" aria-busy={busy || undefined}>
            {styleChoices.map((choice) => (
              <button
                key={choice.style}
                type="button"
                role="radio"
                aria-checked={wallpaper?.style === choice.style}
                disabled={busy || !wallpaper}
                onClick={() => chooseStyle(choice.style)}
              >
                {choice.label}
              </button>
            ))}
          </div>
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
    <Group
      title="本机屏幕"
      footnote="本机屏幕是直接接在这台 NAS 上的显示器，用手机或电脑浏览器访问时不会息屏。黑屏时视频停止播放，显示器背光仍按它自己的亮度点亮。"
    >
      <div className={`${styles.setRow} ${styles.pzRow}`}>
        <IconTile tone="sleep" icon={Moon} />
        <div className={styles.setRowText}>
          <span>息屏</span>
          <small>屏保播放多久后让屏幕变黑</small>
        </div>
        <div className={styles.pzSegmented} role="radiogroup" aria-label="息屏时间" aria-busy={saving || undefined}>
          {sleepChoices.map(({ minutes, label }) => (
            <button
              key={minutes}
              type="button"
              role="radio"
              aria-checked={sleepAfter === minutes}
              disabled={saving || sleepAfter === undefined}
              onClick={() => void choose(minutes)}
            >
              {label}
            </button>
          ))}
        </div>
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
    <figure className={`${styles.pzTimeline} ${sleeps ? "" : styles.never}`}>
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
      <figcaption>
        {sleeps
          ? `登录页没有屏保，${minutesText(blackAt)}后直接变黑。按任意键、移动鼠标或触摸屏幕即可唤醒。`
          : "屏保会一直播放，登录页一直亮着。"}
      </figcaption>
    </figure>
  );
}
