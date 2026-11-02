import { CircleAlert, Eye, EyeOff, LoaderCircle } from "lucide-react";
import { type InputHTMLAttributes, type ReactNode, useEffect, useId, useRef, useState } from "react";

import { bootSplashShowing } from "../boot/BootSplash";
import { startIdentLockup } from "../boot/bootIdent";
import { formatClock, formatDate } from "../lib/time";
import styles from "./auth.module.css";
import { startMeteorShower } from "./meteorShower";

// AuthStage is the late-night broadcast behind sign-in, setup and password
// reset: meteor rain, the boot ident's lockup on one side and the screen's own
// content on the other, under CRT scanlines and on-screen display text. The
// canvases keep running while the screens inside it change, and pause while
// the stage is not rendered: the local console's screen saver and black
// screen stop the page beneath them from rendering, but not its animation
// frames.
export function AuthStage({ children }: { children: ReactNode }) {
  const stageRef = useRef<HTMLElement>(null);
  const skyRef = useRef<HTMLCanvasElement>(null);
  const lockupRef = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const sky = skyRef.current && startMeteorShower(skyRef.current);
    const lockup = lockupRef.current && startIdentLockup(lockupRef.current, bootSplashShowing);
    const shown =
      stageRef.current && typeof IntersectionObserver !== "undefined"
        ? new IntersectionObserver(([entry]) => {
            for (const loop of [sky, lockup]) {
              if (entry?.isIntersecting) loop?.resume();
              else loop?.pause();
            }
          })
        : undefined;
    if (stageRef.current) shown?.observe(stageRef.current);
    return () => {
      shown?.disconnect();
      sky?.stop();
      lockup?.stop();
    };
  }, []);
  return (
    <main ref={stageRef} className={styles.authShell}>
      <canvas ref={skyRef} className={styles.authBackdrop} aria-hidden="true" />
      <div className={styles.authLayout}>
        <div className={styles.authBrand}>
          <canvas ref={lockupRef} aria-hidden="true" />
        </div>
        <div className={styles.authSide}>{children}</div>
      </div>
      <div className={styles.authOsd} aria-hidden="true">
        <span className={styles.authOsdChannel}>CH 01</span>
        <span className={styles.authOsdVersion}>A-NAS v1.0.1 PREVIEW</span>
        <OsdClock />
      </div>
      <div className={styles.authCrt} aria-hidden="true" />
    </main>
  );
}

// A camcorder date stamp showing the NAS clock, in the product's own clock and date formats.
function OsdClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 10_000);
    return () => window.clearInterval(timer);
  }, []);
  return (
    <span className={styles.authOsdClock}>
      <span>{formatClock(now)}</span>
      <span>{formatDate(now, { year: "numeric", month: "2-digit", day: "2-digit" })}</span>
    </span>
  );
}

export function AuthHero({ eyebrow, title, lede }: { eyebrow: string; title: string; lede?: string }) {
  return (
    <header className={styles.authHero}>
      <p className={styles.authEyebrow}>{eyebrow}</p>
      <h1>{title}</h1>
      {lede && <p className={styles.authLede}>{lede}</p>}
    </header>
  );
}

export function AuthError({ text }: { text: string }) {
  return (
    <p className={styles.authError} role="alert">
      <CircleAlert aria-hidden="true" />
      {text}
    </p>
  );
}

// The primary action carries the ident's three sunset racing stripes.
export function AuthSubmit({
  label,
  submitting = false,
  onClick,
}: {
  label: string;
  submitting?: boolean;
  onClick?: () => void;
}) {
  return (
    <button className={styles.authSubmit} type={onClick ? "button" : "submit"} disabled={submitting} onClick={onClick}>
      {submitting ? (
        <>
          <LoaderCircle className="spinning" aria-hidden="true" />
          处理中…
        </>
      ) : (
        <span>{label}</span>
      )}
      <span className={styles.authStripes} aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
    </button>
  );
}

type AuthFieldProps = { label: string; icon: ReactNode; hint?: string } & Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "id" | "placeholder"
>;

// A pill input whose label is read by assistive technology and shown as the placeholder.
export function AuthField({ label, icon, hint, type = "text", ...input }: AuthFieldProps) {
  const id = useId(),
    hintId = useId();
  const [revealed, setRevealed] = useState(false);
  const secret = type === "password";
  return (
    <div className={styles.authField}>
      <div className={styles.authInput}>
        {icon}
        <label htmlFor={id} className="sr-only">
          {label}
        </label>
        <input
          id={id}
          type={secret && revealed ? "text" : type}
          placeholder={label}
          aria-describedby={hint ? hintId : undefined}
          {...input}
        />
        {secret && (
          <button
            type="button"
            className={styles.authReveal}
            aria-label={revealed ? "隐藏密码" : "显示密码"}
            aria-pressed={revealed}
            onClick={() => setRevealed(!revealed)}
          >
            {revealed ? <EyeOff /> : <Eye />}
          </button>
        )}
      </div>
      {hint && <small id={hintId}>{hint}</small>}
    </div>
  );
}
