import { createRenderer, type CrtParams } from "./crtRenderer";

// The A-NAS startup ident: an 80s TV-station logo on a CRT. A drive array is
// built bottom-up, the roof is drawn over it, the name locks in like a VHS
// signal and the set switches off into the desktop. The scene is drawn at a
// fixed 4:3 resolution to match the local console screen.

export const IDENT_WIDTH = 1024;
export const IDENT_HEIGHT = 768;
// Seconds from power-on: the CRT switches off at `exit`, then the overlay fades out.
export const BOOT_TIMELINE = { exit: 6.2, fadeStart: 6.95, end: 7.4 } as const;

const W = IDENT_WIDTH;
const H = IDENT_HEIGHT;

const clamp01 = (x: number) => (x < 0 ? 0 : x > 1 ? 1 : x);
const progress = (t: number, from: number, to: number) => clamp01((t - from) / (to - from));
const easeOutExpo = (x: number) => (x >= 1 ? 1 : 1 - Math.pow(2, -10 * x));
const easeOutCubic = (x: number) => 1 - Math.pow(1 - x, 3);
const easeInCubic = (x: number) => x * x * x;
const easeInOutCubic = (x: number) => (x < .5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2);
const easeOutBack = (x: number) => 1 + 2.70158 * Math.pow(x - 1, 3) + 1.70158 * Math.pow(x - 1, 2);
const hash = (n: number) => {
  const s = Math.sin(n * 127.1 + 311.7) * 43758.5453;
  return s - Math.floor(s);
};

// Sunset palette: the roof runs outer -> inner, the array top -> bottom runs inner -> outer.
const roofColors = ["#ff2f6d", "#ff7b1c", "#ffd23a"] as const;
const wordColors = ["#fff8ec", "#ffe0c2"] as const;

function arrayColor(k: number): string {
  const stops = [roofColors[2], roofColors[1], roofColors[0]].map((hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)));
  const [a, b, f] = k < .5 ? [stops[0], stops[1], k * 2] : [stops[1], stops[2], (k - .5) * 2];
  return `rgb(${a.map((v, i) => Math.round(v + (b[i] - v) * f)).join(",")})`;
}

// ---------------------------------------------------------------------------
// Lettering. "A-NAS" is custom extended lettering (cap height 130, stem 30)
// built from rounded polygons; the tagline is monoline vector glyphs, so the
// ident needs no web fonts on an offline console.

type Corner = [x: number, y: number, radius: number];

function roundPolygon(path: Path2D, corners: Corner[]) {
  const last = corners[corners.length - 1], first = corners[0];
  path.moveTo((last[0] + first[0]) / 2, (last[1] + first[1]) / 2);
  corners.forEach(([x, y, r], i) => {
    const [nx, ny] = corners[(i + 1) % corners.length];
    if (r > 0) path.arcTo(x, y, nx, ny, r); else path.lineTo(x, y);
  });
  path.closePath();
}

const CAP = 130, STEM = 30, RAD = 34, WORD_WIDTH = 632, WORD_PAD = 30;

function addWordmark(path: Path2D) {
  const at = (x0: number) => (x: number, y: number, r: number): Corner => [x0 + x, y, r];
  const letterA = (x0: number) => {
    const q = at(x0), w = 128, t = STEM;
    roundPolygon(path, [q(0, CAP, 0), q(0, 0, RAD), q(w, 0, RAD), q(w, CAP, 0), q(w - t, CAP, 0), q(w - t, 88, 5), q(t, 88, 5), q(t, CAP, 0)]);
    roundPolygon(path, [q(t, t, 10), q(w - t, t, 10), q(w - t, 62, 3), q(t, 62, 3)]);
  };
  const letterN = (x0: number) => {
    const q = at(x0), w = 128, t = STEM, d = 38;
    const inner1 = (w - t - d) * CAP / (w - d), inner2 = t * CAP / (w - d);
    roundPolygon(path, [q(0, CAP, 0), q(0, 0, RAD), q(d, 0, 0), q(w - t, inner1, 3), q(w - t, 0, 0), q(w, 0, 0),
      q(w, CAP, RAD), q(w - d, CAP, 0), q(t, inner2, 3), q(t, CAP, 0)]);
  };
  const letterS = (x0: number) => {
    const q = at(x0), w = 124, t = STEM;
    roundPolygon(path, [q(w, 0, 0), q(w, t, 0), q(t, t, 6), q(t, 52, 6), q(w, 52, RAD), q(w, CAP, RAD), q(0, CAP, 0),
      q(0, CAP - t, 0), q(w - t, CAP - t, 6), q(w - t, 78, 6), q(0, 78, RAD), q(0, 0, RAD)]);
  };
  letterA(0);
  roundPolygon(path, [[146, 54, 5], [198, 54, 5], [198, 78, 5], [146, 78, 5]]);
  letterN(216);
  letterA(362);
  letterS(508);
}

function renderWordmark(c: CanvasRenderingContext2D) {
  c.setTransform(1, 0, 0, 1, WORD_PAD, WORD_PAD);
  const path = new Path2D();
  addWordmark(path);
  // Black keyline so the name sits cleanly on top of the drive array.
  c.lineJoin = "round";
  c.lineWidth = 18;
  c.strokeStyle = "#000";
  c.stroke(path);
  const fill = c.createLinearGradient(0, 0, 0, CAP);
  fill.addColorStop(0, wordColors[0]);
  fill.addColorStop(1, wordColors[1]);
  c.fillStyle = fill;
  c.fill(path, "evenodd");
  // Speed-stripe cuts through the lower half, echoing the array.
  c.globalCompositeOperation = "destination-out";
  for (const [y, h] of [[93, 3], [106, 4], [118, 5.5]]) c.fillRect(-60, y, WORD_WIDTH + 120, h);
  c.globalCompositeOperation = "source-over";
  c.setTransform(1, 0, 0, 1, 0, 0);
}

interface GlyphStroke { points: [number, number][]; closed?: boolean }

export const TAGLINE = "HOME STORAGE";
const GW = 1.32, TRACKING = .62, SPACE = .8;
// Units of cap height; x runs to GW for a full-width glyph.
export const taglineGlyphs: Record<string, GlyphStroke[]> = {
  " ": [],
  A: [{ points: [[0, 1], [0, 0], [GW, 0], [GW, 1]] }, { points: [[0, .56], [GW, .56]] }],
  E: [{ points: [[GW, 0], [0, 0], [0, 1], [GW, 1]] }, { points: [[0, .5], [GW * .82, .5]] }],
  G: [{ points: [[GW, 0], [0, 0], [0, 1], [GW, 1], [GW, .52], [GW * .5, .52]] }],
  H: [{ points: [[0, 0], [0, 1]] }, { points: [[GW, 0], [GW, 1]] }, { points: [[0, .5], [GW, .5]] }],
  M: [{ points: [[0, 1], [0, 0], [GW / 2, .62], [GW, 0], [GW, 1]] }],
  O: [{ points: [[0, 0], [GW, 0], [GW, 1], [0, 1]], closed: true }],
  R: [{ points: [[0, 1], [0, 0], [GW, 0], [GW, .52], [0, .52]] }, { points: [[GW * .5, .52], [GW, 1]] }],
  S: [{ points: [[GW, 0], [0, 0], [0, .5], [GW, .5], [GW, 1], [0, 1]] }],
  T: [{ points: [[0, 0], [GW, 0]] }, { points: [[GW / 2, 0], [GW / 2, 1]] }],
};
const advance = (ch: string) => (ch === " " ? SPACE : GW + TRACKING);
const taglineWidth = (text: string) => [...text].reduce((sum, ch) => sum + advance(ch), 0) - TRACKING;

function strokeGlyph(c: CanvasRenderingContext2D, stroke: GlyphStroke, x: number, y: number, size: number) {
  const pts = stroke.points.map(([px, py]) => [x + px * size, y + py * size] as const);
  const r = size * .2;
  c.beginPath();
  if (stroke.closed) {
    const last = pts[pts.length - 1], first = pts[0];
    c.moveTo((last[0] + first[0]) / 2, (last[1] + first[1]) / 2);
    pts.forEach(([px, py], i) => {
      const [nx, ny] = pts[(i + 1) % pts.length];
      c.arcTo(px, py, nx, ny, r);
    });
    c.closePath();
  } else {
    c.moveTo(pts[0][0], pts[0][1]);
    for (let i = 1; i < pts.length - 1; i++) c.arcTo(pts[i][0], pts[i][1], pts[i + 1][0], pts[i + 1][1], r);
    c.lineTo(pts[pts.length - 1][0], pts[pts.length - 1][1]);
  }
  c.stroke();
}

// ---------------------------------------------------------------------------
// Scene

const ICON = { cx: 512, base: 318, apex0: 116, step: 46, slope: 1.125, lineWidth: 20 };
const ARRAY = { top: 338, height: 17, pitch: 32, count: 7, left: 332, right: 692, leds: [1, 3, 5] };
const WORD = { cx: 512, top: 500, scale: .84 };
const TAG = { y: 662, cap: 16 };
const COMPOSITION = { scale: .94, cx: 512, cy: 392 };
const driveStart = (order: number) => .5 + order * .11;

interface IdentScene {
  canvas: HTMLCanvasElement;
  glow: HTMLCanvasElement;
  draw(t: number, now: number): void;
}

function makeCanvas(width: number, height: number) {
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  return canvas;
}

function createIdentScene(): IdentScene | null {
  const canvas = makeCanvas(W, H), glow = makeCanvas(W / 4, H / 4);
  const word = makeCanvas(WORD_WIDTH + WORD_PAD * 2, CAP + WORD_PAD * 2), shine = makeCanvas(word.width, word.height);
  const sceneContext = canvas.getContext("2d"), glowContext = glow.getContext("2d");
  const wordContext = word.getContext("2d"), shineContext = shine.getContext("2d");
  if (!sceneContext || !glowContext || !wordContext || !shineContext) return null;
  const c: CanvasRenderingContext2D = sceneContext, g: CanvasRenderingContext2D = glowContext, sc: CanvasRenderingContext2D = shineContext;
  renderWordmark(wordContext);
  const wordX = WORD.cx - word.width * WORD.scale / 2, wordY = WORD.top - WORD_PAD * WORD.scale;

  // Each drive slides in from alternating sides, seats with a flash and later shows an activity LED.
  function drawArray(t: number, now: number) {
    const { top, height: h, pitch, count, left, right } = ARRAY;
    for (let i = 0; i < count; i++) {
      const order = count - 1 - i, start = driveStart(order), k = progress(t, start, start + .42);
      if (k <= 0) continue;
      const e = easeOutExpo(k), dir = order % 2 ? 1 : -1, off = dir * (1 - e) * 900;
      const y = top + i * pitch, color = arrayColor(i / (count - 1)), xl = left + off, xr = right + off;
      c.fillStyle = color;
      c.fillRect(xl, y, xr - xl, h);
      const trail = (1 - e) * 650;
      if (trail > 1) {
        const tail = dir < 0 ? xl : xr, gradient = c.createLinearGradient(tail + dir * -trail, 0, tail, 0);
        gradient.addColorStop(0, "rgba(0,0,0,0)");
        gradient.addColorStop(1, color);
        c.fillStyle = gradient;
        c.fillRect(Math.min(tail, tail - dir * trail), y + 3, trail, h - 6);
      }
      const seated = t - start - .2;
      if (seated > 0 && seated < .2) {
        c.fillStyle = `rgba(255,255,255,${(.55 * (1 - seated / .2)).toFixed(3)})`;
        c.fillRect(xl, y, xr - xl, h);
      }
      if (ARRAY.leds.includes(i)) {
        const nx = xr - 38;
        c.fillStyle = "#000";
        c.fillRect(nx, y + 4, 16, h - 8);
        if (t > 3.8 && hash(Math.floor(now * 7) + i * 13.7) > .42) {
          c.fillStyle = "#eafff2";
          c.fillRect(nx + 3, y + 6, 10, h - 12);
        }
      }
    }
  }

  function drawRoof(t: number) {
    const { cx, base, apex0, step, slope, lineWidth } = ICON;
    c.save();
    c.beginPath();
    c.rect(0, 0, W, base + 1);
    c.clip();
    c.lineWidth = lineWidth;
    c.lineJoin = "miter";
    c.miterLimit = 10;
    c.lineCap = "butt";
    for (let k = 0; k < 3; k++) {
      const p = easeInOutCubic(progress(t, 1.35 + k * .15, 1.8 + k * .15));
      if (p <= 0) continue;
      const ay = apex0 + k * step, dy = base + 30 - ay, dx = dy * slope, length = 2 * Math.hypot(dx, dy);
      c.strokeStyle = roofColors[k];
      c.setLineDash([length * p, length + 1]);
      c.beginPath();
      c.moveTo(cx - dx, base + 30);
      c.lineTo(cx, ay);
      c.lineTo(cx + dx, base + 30);
      c.stroke();
    }
    c.setLineDash([]);
    const pop = progress(t, 2.05, 2.4);
    if (pop > 0) {
      const s = easeOutBack(pop), ay = apex0 + 2 * step + 40, hw = (base - ay) * slope, cy = (ay + base * 2) / 3;
      c.translate(cx, cy);
      c.scale(s, s);
      c.translate(-cx, -cy);
      c.fillStyle = roofColors[2];
      c.beginPath();
      c.moveTo(cx, ay);
      c.lineTo(cx + hw, base);
      c.lineTo(cx - hw, base);
      c.closePath();
      c.fill();
    }
    c.restore();
  }

  function drawWord(t: number) {
    if (t < 2.25) return;
    let source = word;
    const sweep = progress(t, 3.75, 4.4);
    if (sweep > 0 && sweep < 1) {
      sc.globalCompositeOperation = "source-over";
      sc.clearRect(0, 0, shine.width, shine.height);
      sc.drawImage(word, 0, 0);
      sc.globalCompositeOperation = "source-atop";
      const bx = -140 + sweep * (shine.width + 280), band = sc.createLinearGradient(bx - 70, 0, bx + 70, -40);
      band.addColorStop(0, "rgba(255,255,255,0)");
      band.addColorStop(.5, "rgba(255,255,255,.95)");
      band.addColorStop(1, "rgba(255,255,255,0)");
      sc.fillStyle = band;
      sc.fillRect(0, 0, shine.width, shine.height);
      source = shine;
    }
    // VHS lock-in: horizontal slices slide in from alternating sides.
    const slices = 14, sliceH = source.height / slices, k = WORD.scale;
    for (let i = 0; i < slices; i++) {
      const start = 2.25 + (i % 2) * .05 + hash(i) * .16, e = easeOutCubic(progress(t, start, start + .5));
      if (e <= 0) continue;
      const dx = (1 - e) * (i % 2 ? 1 : -1) * (240 + 200 * hash(i + 7));
      c.globalAlpha = clamp01(e * 1.6);
      c.drawImage(source, 0, i * sliceH, source.width, sliceH, wordX + dx, wordY + i * sliceH * k, source.width * k, sliceH * k + .5);
    }
    c.globalAlpha = 1;
  }

  // Tagline types out between two mini arrays that grow outward.
  function drawTagline(t: number) {
    const lines = easeOutCubic(progress(t, 3.0, 3.45)), shown = Math.floor(progress(t, 3.05, 3.5) * TAGLINE.length + .001);
    if (lines <= 0 && shown <= 0) return;
    const width = taglineWidth(TAGLINE) * TAG.cap, x0 = ICON.cx - width / 2, top = TAG.y - TAG.cap / 2;
    c.save();
    c.strokeStyle = wordColors[1];
    c.lineWidth = TAG.cap * .16;
    c.lineJoin = "round";
    c.lineCap = "butt";
    let pen = x0;
    [...TAGLINE].forEach((ch, i) => {
      if (i < shown) for (const stroke of taglineGlyphs[ch]) strokeGlyph(c, stroke, pen, top, TAG.cap);
      pen += advance(ch) * TAG.cap;
    });
    c.restore();
    const gap = 26, length = 118 * lines;
    for (let j = 0; j < 3; j++) {
      c.fillStyle = arrayColor(j / 2);
      const y = TAG.y - 8 + j * 6;
      c.fillRect(x0 - gap - length, y, length, 3);
      c.fillRect(x0 + width + gap, y, length, 3);
    }
  }

  function sparkle(x: number, y: number, k: number, size: number) {
    const a = Math.sin(Math.PI * clamp01(k));
    if (a <= 0) return;
    c.save();
    c.globalCompositeOperation = "lighter";
    c.translate(x, y);
    c.rotate(k * .5);
    const core = c.createRadialGradient(0, 0, 0, 0, 0, size * .28 * a);
    core.addColorStop(0, "rgba(255,255,255,1)");
    core.addColorStop(1, "rgba(255,255,255,0)");
    c.fillStyle = core;
    c.beginPath();
    c.arc(0, 0, size * .28 * a, 0, Math.PI * 2);
    c.fill();
    c.fillStyle = "#fff";
    const ray = (length: number, width: number) => {
      c.beginPath();
      c.moveTo(-length, 0);
      c.lineTo(0, -width);
      c.lineTo(length, 0);
      c.lineTo(0, width);
      c.closePath();
      c.fill();
    };
    ray(size * a, size * .045 * a);
    c.rotate(Math.PI / 2);
    ray(size * .75 * a, size * .045 * a);
    c.rotate(Math.PI / 4);
    ray(size * .3 * a, size * .03 * a);
    c.rotate(Math.PI / 2);
    ray(size * .3 * a, size * .03 * a);
    c.restore();
  }

  return {
    canvas,
    glow,
    draw(t, now) {
      c.setTransform(1, 0, 0, 1, 0, 0);
      c.globalCompositeOperation = "source-over";
      c.globalAlpha = 1;
      c.fillStyle = "#000";
      c.fillRect(0, 0, W, H);
      const s = COMPOSITION.scale;
      c.setTransform(s, 0, 0, s, W / 2 - s * COMPOSITION.cx, H / 2 - s * COMPOSITION.cy);
      drawArray(t, now);
      drawRoof(t);
      drawWord(t);
      drawTagline(t);
      sparkle(ICON.cx, ICON.apex0 - 10, progress(t, 3.65, 4.3), 140);
      sparkle(wordX + (WORD_PAD + WORD_WIDTH) * WORD.scale - 4, wordY + WORD_PAD * WORD.scale + 2, progress(t, 4.0, 4.6), 70);
      c.setTransform(1, 0, 0, 1, 0, 0);

      g.clearRect(0, 0, glow.width, glow.height);
      g.filter = "blur(5px)";
      g.drawImage(canvas, 0, 0, glow.width, glow.height);
      g.filter = "none";
    },
  };
}

// CRT effect strength over the timeline: beam and static at power-on, colour
// fringing while the name locks in, a bloom flash at the climax, one tracking
// roll, then the switch-off collapse to a line and a dot.
export function crtParams(t: number, now: number): CrtParams {
  const p: CrtParams = { open: 1, openX: 1, beam: 0, flash: 0, aberr: 1.5, bloom: .85, noise: .35, roll: -2, stat: 0 };
  if (t < .12) {
    p.open = .002;
    p.beam = progress(t, .02, .12);
  } else if (t < .6) {
    p.open = .002 + .998 * easeOutExpo(progress(t, .12, .42));
    p.beam = 1 - progress(t, .12, .3);
  }
  if (t >= .12) p.stat = .85 * (1 - progress(t, .22, .66));
  if (t > 2.25) {
    const settle = progress(t, 2.25, 3.1);
    p.aberr += 7 * (1 - settle);
    p.noise += 1.8 * (1 - settle);
  }
  const climax = t - 3.65;
  if (climax > 0) {
    p.bloom += 1.6 * Math.exp(-climax * 3.2);
    p.flash += .2 * Math.exp(-climax * 14);
    p.aberr += 3 * Math.exp(-climax * 5);
  }
  if (t > 4.6 && t < 5.6) {
    p.roll = 1.15 - 1.3 * progress(t, 4.6, 5.6);
    p.noise += 1.2;
  }
  const { exit } = BOOT_TIMELINE;
  if (t >= exit) {
    const collapse = progress(t, exit, exit + .16), shrink = progress(t, exit + .16, exit + .4), fade = progress(t, exit + .4, exit + .72);
    p.open = 1 - .998 * easeInCubic(collapse);
    p.openX = 1 - .999 * easeInOutCubic(shrink);
    p.beam = fade >= 1 ? 0 : Math.min(1, collapse * 1.5) * (1 - fade);
    p.bloom += collapse * .8;
  }
  p.aberr += .35 * Math.sin(now * 3.1);
  return p;
}

// ---------------------------------------------------------------------------
// Playback

export interface BootIdentHooks {
  onFade(opacity: number): void;
  onDone(): void;
}

export interface BootIdentPlayer {
  skip(): void;
  stop(): void;
}

// Returns null when the browser cannot draw the ident, so the caller can go straight to the desktop.
export function startBootIdent(canvas: HTMLCanvasElement, hooks: BootIdentHooks): BootIdentPlayer | null {
  const scene = createIdentScene();
  const renderer = scene && createRenderer(canvas);
  if (!scene || !renderer) return null;

  let origin = performance.now(), frame = 0, running = true;
  const fit = () => {
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.max(1, Math.min(1600, Math.round(canvas.getBoundingClientRect().width * dpr)));
    canvas.width = width;
    canvas.height = Math.round(width * 3 / 4);
  };
  const halt = () => {
    running = false;
    cancelAnimationFrame(frame);
    window.removeEventListener("resize", fit);
  };
  const finish = () => {
    if (!running) return;
    halt();
    hooks.onDone();
  };
  const tick = (ms: number) => {
    const t = (ms - origin) / 1000, now = ms / 1000;
    if (t >= BOOT_TIMELINE.end) return finish();
    try {
      scene.draw(t, now);
      renderer.render(scene.canvas, scene.glow, crtParams(t, now), now);
    } catch {
      return finish();
    }
    if (renderer.lost) return finish();
    hooks.onFade(1 - progress(t, BOOT_TIMELINE.fadeStart, BOOT_TIMELINE.end));
    frame = requestAnimationFrame(tick);
  };

  fit();
  window.addEventListener("resize", fit);
  frame = requestAnimationFrame(tick);
  return {
    // The first skip jumps to the switch-off; a second one during the switch-off ends it at once.
    skip() {
      const now = performance.now();
      if ((now - origin) / 1000 >= BOOT_TIMELINE.exit) finish();
      else origin = now - BOOT_TIMELINE.exit * 1000;
    },
    stop: halt,
  };
}
