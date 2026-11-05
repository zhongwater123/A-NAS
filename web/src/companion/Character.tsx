import { AnimatePresence, MotionConfig, motion } from "motion/react";
import { useEffect, useId, useState } from "react";

import concerned from "./assets/dafeiyu-concerned.webp";
import happy from "./assets/dafeiyu-happy.webp";
import idle from "./assets/dafeiyu-idle.webp";
import thinking from "./assets/dafeiyu-thinking.webp";
import waiting from "./assets/dafeiyu-waiting.webp";
import wave from "./assets/dafeiyu-wave.webp";
import working from "./assets/dafeiyu-working.webp";
import { type PetAction, taskFeeling } from "./behaviour";
import styles from "./companion.module.css";
import type { Character as CharacterKind } from "./preferences";
import type { TaskPhase } from "./taskState";

// One horizontal strip per pose, built by scripts/import-companion-art.py
// (frame counts and timing are recorded in assets/dafeiyu.source.json). Loops
// start and end at rest; a one-shot pose plays once, then the character idles
// until another pose is asked for.
const strips: Record<PetAction, { sheet: string; frames: number; seconds: number; loop: boolean }> = {
  idle: { sheet: idle, frames: 60, seconds: 10, loop: true },
  thinking: { sheet: thinking, frames: 60, seconds: 10, loop: true },
  working: { sheet: working, frames: 60, seconds: 10, loop: true },
  waiting: { sheet: waiting, frames: 60, seconds: 10, loop: true },
  happy: { sheet: happy, frames: 42, seconds: 7, loop: false },
  concerned: { sheet: concerned, frames: 30, seconds: 5, loop: false },
  wave: { sheet: wave, frames: 24, seconds: 2, loop: false },
  // The set has no walking clips yet.
  left: { sheet: idle, frames: 60, seconds: 10, loop: true },
  right: { sheet: idle, frames: 60, seconds: 10, loop: true },
};

// Each strip is its own image: fetch them soon after the character first
// shows, so a pose's first appearance does not fade into an empty frame.
let preloaded = false;
function preloadStrips() {
  if (preloaded) return;
  preloaded = true;
  for (const strip of Object.values(strips)) new Image().src = strip.sheet;
}

// Code-native experimental artwork, intentionally separate from behaviour.
// The earlier generated concept sheets are not animation-ready assets.
export function Character({
  kind,
  asleep = false,
  phase = "idle",
  action,
}: {
  kind: CharacterKind;
  asleep?: boolean;
  phase?: TaskPhase;
  action?: PetAction;
}) {
  const requested = asleep ? "idle" : (action ?? taskFeeling[phase].action);
  const [shown, setShown] = useState({ requested, finished: false });
  if (shown.requested !== requested) setShown({ requested, finished: false });
  useEffect(() => {
    if (kind !== "naiwa") return;
    const timer = window.setTimeout(preloadStrips, 1500);
    return () => window.clearTimeout(timer);
  }, [kind]);
  if (kind === "naiwa") {
    const pose = shown.requested === requested && shown.finished ? "idle" : requested;
    const strip = strips[pose];
    return (
      <MotionConfig reducedMotion="user">
        <motion.span
          aria-hidden="true"
          className={styles.avatar}
          data-action={pose}
          initial={false}
          animate={{
            x: pose === "left" ? -20 : pose === "right" ? 20 : 0,
            y: pose === "happy" ? -7 : 0,
            rotate: pose === "wave" ? -3 : 0,
          }}
          transition={{ type: "spring", stiffness: 170, damping: 18 }}
        >
          <AnimatePresence initial={false}>
            <motion.span
              key={pose}
              className={styles.petSprite}
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.18 }}
              style={{
                backgroundImage: `url(${strip.sheet})`,
                backgroundSize: `${strip.frames * 100}% 100%`,
                animationDuration: `${strip.seconds}s`,
                animationTimingFunction: `steps(${strip.frames}, jump-none)`,
                animationIterationCount: strip.loop ? "infinite" : 1,
                animationPlayState: asleep ? "paused" : "running",
              }}
              onAnimationEnd={(event) => {
                if (event.target === event.currentTarget && !strip.loop) setShown({ requested, finished: true });
              }}
            />
          </AnimatePresence>
        </motion.span>
      </MotionConfig>
    );
  }
  return <LegacyCharacter kind={kind} asleep={asleep} />;
}
function LegacyCharacter({ kind, asleep }: { kind: CharacterKind; asleep: boolean }) {
  const id = useId().replace(/:/g, "");
  const body = `${id}-body`;
  const light = `${id}-light`;
  return (
    <svg className={styles.character} viewBox="0 0 200 200" aria-hidden="true">
      <defs>
        <radialGradient id={body} cx="35%" cy="25%" r="80%">
          <stop offset="0" stopColor={kind === "glow" ? "#ffffff" : "#fce7be"} />
          <stop offset="0.65" stopColor={kind === "glow" ? "#cbf3ee" : "#deb680"} />
          <stop offset="1" stopColor={kind === "glow" ? "#77bfc2" : "#b67b49"} />
        </radialGradient>
        <radialGradient id={light}>
          <stop offset="0" stopColor="#fff9d9" stopOpacity="1" />
          <stop offset="1" stopColor="#ffcb82" stopOpacity="0" />
        </radialGradient>
      </defs>
      <ellipse cx="100" cy="180" rx="47" ry="6" fill="#263e50" opacity="0.1" />
      {kind === "glow" ? (
        <>
          <path
            className={styles.finLeft}
            d="M62 92C11 52 10 116 43 143C58 158 31 168 25 153C46 193 85 135 62 92Z"
            fill={`url(#${body})`}
            stroke="#c4e9e7"
          />
          <path
            className={styles.finRight}
            d="M138 92C189 52 190 116 157 143C142 158 169 168 175 153C154 193 115 135 138 92Z"
            fill={`url(#${body})`}
            stroke="#c4e9e7"
          />
          <path
            d="M100 25C132 26 153 69 149 108C146 149 124 170 100 169C76 170 54 149 51 108C47 69 68 26 100 25Z"
            fill={`url(#${body})`}
            stroke="#fff"
            strokeWidth="2"
          />
          <ellipse cx="100" cy="123" rx="33" ry="37" fill={`url(#${light})`} />
          <path d="M77 47Q88 34 103 37" fill="none" stroke="#fff" strokeWidth="5" strokeLinecap="round" opacity="0.8" />
          <path d="M95 162Q79 187 106 184Q118 180 110 167" fill={`url(#${body})`} />
        </>
      ) : (
        <>
          <path
            className={styles.finRight}
            d="M130 133C192 157 197 62 149 42C111 26 121 71 145 76C174 83 159 113 137 106Z"
            fill="#bc8556"
            stroke="#a56c3d"
            strokeWidth="2"
          />
          <path d="M153 53Q185 102 147 127" fill="none" stroke="#e5b97f" strokeWidth="9" strokeLinecap="round" />
          <circle cx="64" cy="56" r="20" fill="#bb8155" />
          <circle cx="64" cy="56" r="12" fill="#ecc0a2" />
          <circle cx="126" cy="56" r="20" fill="#bb8155" />
          <circle cx="126" cy="56" r="12" fill="#ecc0a2" />
          <ellipse cx="95" cy="117" rx="58" ry="61" fill={`url(#${body})`} />
          <path d="M47 88Q51 39 95 40Q139 40 143 88Q125 74 111 80Q95 54 80 80Q65 75 47 88Z" fill="#b98253" />
          <ellipse cx="95" cy="132" rx="40" ry="38" fill="#fff1d5" />
          <ellipse cx="53" cy="126" rx="13" ry="19" fill="#c28c5e" transform="rotate(-25 53 126)" />
          <ellipse cx="137" cy="126" rx="13" ry="19" fill="#c28c5e" transform="rotate(25 137 126)" />
          <ellipse cx="69" cy="175" rx="17" ry="7" fill="#996139" />
          <ellipse cx="119" cy="175" rx="17" ry="7" fill="#996139" />
        </>
      )}
      <g className={asleep ? styles.sleepyEyes : styles.eyes}>
        {asleep ? (
          <path
            d="M73 91Q79 98 85 91M110 91Q116 98 122 91"
            fill="none"
            stroke="#304452"
            strokeWidth="4"
            strokeLinecap="round"
          />
        ) : (
          <>
            <ellipse cx="79" cy="89" rx="6" ry="9" fill="#253e4f" />
            <ellipse cx="116" cy="89" rx="6" ry="9" fill="#253e4f" />
            <circle cx="81" cy="86" r="2" fill="#fff" />
            <circle cx="118" cy="86" r="2" fill="#fff" />
          </>
        )}
      </g>
      <ellipse cx="65" cy="104" rx="10" ry="5" fill="#f1b7a0" opacity="0.5" />
      <ellipse cx="130" cy="104" rx="10" ry="5" fill="#f1b7a0" opacity="0.5" />
      <path d="M92 103Q98 109 104 103" fill="none" stroke="#64717b" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}
