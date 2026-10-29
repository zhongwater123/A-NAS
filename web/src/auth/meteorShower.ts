// The sign-in backdrop: faint vertical threads of light hang across a black
// screen and meteors run down them, some in the boot ident's sunset colours.
// Everything is drawn on a 2D canvas, so an offline console needs no assets.

export interface MeteorShower {
  stop(): void;
}

const WHITE = "255,244,226";
const COOL = "112,218,226";
const SUNSET = ["255,210,58", "255,123,28", "255,47,109"] as const;

interface Meteor {
  x: number;
  y: number;
  length: number;
  speed: number;
  width: number;
  alpha: number;
  color: string;
}
interface Mote {
  x: number;
  y: number;
  size: number;
  alpha: number;
  phase: number;
  rise: number;
}

const pick = <T>(items: readonly T[]) => items[Math.floor(Math.random() * items.length)];

function tint(warm: number, cool: number): string {
  const roll = Math.random();
  return roll < warm ? pick(SUNSET) : roll < warm + cool ? COOL : WHITE;
}

// A soft round glow per colour, drawn once and stamped at every meteor head.
function glowSprite(color: string): HTMLCanvasElement {
  const sprite = document.createElement("canvas");
  sprite.width = sprite.height = 64;
  const g = sprite.getContext("2d");
  if (g) {
    const fill = g.createRadialGradient(32, 32, 0, 32, 32, 32);
    fill.addColorStop(0, `rgba(${color},1)`);
    fill.addColorStop(0.25, `rgba(${color},.35)`);
    fill.addColorStop(1, `rgba(${color},0)`);
    g.fillStyle = fill;
    g.fillRect(0, 0, 64, 64);
  }
  return sprite;
}

// Two layers of threads cross-fade slowly so single threads appear to breathe.
function threadLayer(width: number, height: number, dpr: number): HTMLCanvasElement {
  const layer = document.createElement("canvas");
  layer.width = Math.round(width * dpr);
  layer.height = Math.round(height * dpr);
  const g = layer.getContext("2d");
  if (!g) return layer;
  g.scale(dpr, dpr);
  const count = Math.round(width / 13);
  for (let i = 0; i < count; i++) {
    const x = Math.round(Math.random() * width) + 0.5,
      bright = Math.random() < 0.12;
    const alpha = bright ? 0.1 + Math.random() * 0.08 : 0.025 + Math.random() * 0.06,
      color = tint(0.14, 0.12);
    // Threads fade towards the middle of the screen, where the form sits.
    const fill = g.createLinearGradient(0, 0, 0, height);
    fill.addColorStop(0, `rgba(${color},${alpha})`);
    fill.addColorStop(0.45, `rgba(${color},${alpha * 0.35})`);
    fill.addColorStop(0.6, `rgba(${color},${alpha * 0.45})`);
    fill.addColorStop(1, `rgba(${color},${alpha * 1.2})`);
    g.fillStyle = fill;
    g.fillRect(x - 0.5, 0, Math.random() < 0.2 ? 1.5 : 1, height);
  }
  return layer;
}

// Returns null when the browser cannot draw; the page keeps its plain black background.
export function startMeteorShower(canvas: HTMLCanvasElement): MeteorShower | null {
  const context = canvas.getContext("2d");
  if (!context) return null;
  const c: CanvasRenderingContext2D = context;
  const still = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  const sprites = new Map<string, HTMLCanvasElement>();
  const sprite = (color: string) => sprites.get(color) ?? sprites.set(color, glowSprite(color)).get(color)!;

  let width = 0,
    height = 0,
    layers: HTMLCanvasElement[] = [],
    meteors: Meteor[] = [],
    motes: Mote[] = [];

  // Most meteors drift; a few streak past quickly and glow brighter.
  const launch = (m: Meteor, anywhere: boolean) => {
    const fast = Math.random() < 0.14;
    m.x = Math.round(Math.random() * width) + 0.5;
    m.length = height * (fast ? 0.3 + Math.random() * 0.35 : 0.08 + Math.random() * 0.22);
    m.speed = fast ? 900 + Math.random() * 700 : 90 + Math.random() * 260;
    m.width = fast ? 1.6 : Math.random() < 0.3 ? 1.2 : 0.8;
    m.alpha = fast ? 0.95 : 0.3 + Math.random() * 0.5;
    m.color = tint(0.32, 0.14);
    m.y = anywhere ? Math.random() * (height + m.length) : -Math.random() * height * 0.8;
    return m;
  };

  const fit = () => {
    const dpr = Math.min(window.devicePixelRatio || 1, 2),
      box = canvas.getBoundingClientRect();
    width = Math.max(1, box.width);
    height = Math.max(1, box.height);
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    c.setTransform(dpr, 0, 0, dpr, 0, 0);
    layers = [threadLayer(width, height, dpr), threadLayer(width, height, dpr)];
    const count = Math.max(18, Math.min(60, Math.round(width / 30)));
    meteors = Array.from({ length: count }, () => launch({} as Meteor, true));
    motes = Array.from({ length: Math.round(width / 40) }, () => ({
      x: Math.random() * width,
      y: Math.random() * height,
      size: 0.6 + Math.random() * 1.4,
      alpha: 0.15 + Math.random() * 0.45,
      phase: Math.random() * Math.PI * 2,
      rise: 4 + Math.random() * 10,
    }));
  };

  const draw = (t: number, dt: number) => {
    c.globalCompositeOperation = "source-over";
    c.globalAlpha = 1;
    c.clearRect(0, 0, width, height);
    const breathe = 0.5 + 0.5 * Math.sin(t * 0.35);
    c.globalAlpha = 0.55 + 0.45 * breathe;
    c.drawImage(layers[0], 0, 0, width, height);
    c.globalAlpha = 1 - 0.45 * breathe;
    c.drawImage(layers[1], 0, 0, width, height);

    c.globalCompositeOperation = "lighter";
    for (const m of motes) {
      m.y -= m.rise * dt;
      if (m.y < -4) {
        m.y = height + 4;
        m.x = Math.random() * width;
      }
      c.globalAlpha = m.alpha * (0.4 + 0.6 * Math.abs(Math.sin(t * 0.8 + m.phase)));
      c.fillStyle = `rgb(${WHITE})`;
      c.fillRect(m.x, m.y, m.size, m.size);
    }

    for (const m of meteors) {
      m.y += m.speed * dt;
      if (m.y - m.length > height) launch(m, false);
      if (m.y < 0) continue;
      const tail = c.createLinearGradient(0, m.y - m.length, 0, m.y);
      tail.addColorStop(0, `rgba(${m.color},0)`);
      tail.addColorStop(0.85, `rgba(${m.color},${m.alpha * 0.55})`);
      tail.addColorStop(1, `rgba(${m.color},${m.alpha})`);
      c.globalAlpha = 1;
      c.fillStyle = tail;
      c.fillRect(m.x - m.width / 2, m.y - m.length, m.width, m.length);
      const glow = m.width * (m.speed > 800 ? 22 : 12);
      c.globalAlpha = m.alpha * 0.8;
      c.drawImage(sprite(m.color), m.x - glow / 2, m.y - glow / 2, glow, glow);
    }
    c.globalCompositeOperation = "source-over";
    c.globalAlpha = 1;
  };

  let frame = 0,
    last = performance.now(),
    running = true;
  const stop = () => {
    running = false;
    cancelAnimationFrame(frame);
    window.removeEventListener("resize", resize);
  };
  // The sky is decoration: if drawing ever fails it stops and leaves the black background.
  const attempt = (paint: () => void) => {
    try {
      paint();
      return true;
    } catch {
      stop();
      return false;
    }
  };
  // Time runs on the clock; after a hidden tab resumes, the step is capped rather than replayed.
  const tick = (now: number) => {
    if (!running || !attempt(() => draw(now / 1000, Math.min(0.05, (now - last) / 1000)))) return;
    last = now;
    frame = requestAnimationFrame(tick);
  };
  const resize = () =>
    attempt(() => {
      fit();
      if (still) draw(0, 0);
    });

  window.addEventListener("resize", resize);
  if (!resize()) return null;
  if (!still) frame = requestAnimationFrame(tick);
  return { stop };
}
