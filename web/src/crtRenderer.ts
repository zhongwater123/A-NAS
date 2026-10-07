// Draws the boot ident scene onto the visible canvas. With WebGL it adds a CRT
// look (curvature, scanlines, RGB phosphor mask, colour fringing, bloom, VHS
// jitter, static and the power-on/off beam); without it the scene is copied as is.

export interface CrtParams {
  open: number;
  openX: number;
  beam: number;
  flash: number;
  aberr: number;
  bloom: number;
  noise: number;
  roll: number;
  stat: number;
}

export interface Renderer {
  render(scene: HTMLCanvasElement, glow: HTMLCanvasElement, params: CrtParams, now: number): void;
  readonly lost: boolean;
}

const vertexSource = `attribute vec2 p; varying vec2 vUv; void main(){ vUv = p * .5 + .5; gl_Position = vec4(p, 0., 1.); }`;

const fragmentSource = `
#ifdef GL_FRAGMENT_PRECISION_HIGH
precision highp float;
#else
precision mediump float;
#endif
uniform sampler2D uScene, uGlow;
uniform vec2 uRes;
uniform float uTime, uAberr, uBloom, uOpen, uOpenX, uBeam, uFlash, uNoise, uRoll, uStatic, uScanP;
varying vec2 vUv;
float hash(vec2 p){ return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }
void main(){
  float aspect = uRes.x / uRes.y;
  vec2 c = vUv * 2. - 1.;
  c *= 1. + .045 * dot(c, c);
  vec2 uv = c * .5 + .5;

  vec2 q = abs(c * vec2(aspect, 1.)) - vec2(aspect, 1.) + .1;
  float corner = length(max(q, 0.)) + min(max(q.x, q.y), 0.) - .1;
  float mask = 1. - smoothstep(-.006, .002, corner);

  vec2 s = uv;
  s.y = .5 + (uv.y - .5) / max(uOpen, .0005);
  s.x = .5 + (uv.x - .5) / max(uOpenX, .0005);
  float row = floor(uv.y * 240.);
  s.x += (hash(vec2(row, floor(uTime * 24.))) - .5) * .0022 * uNoise;
  float band = 1. - smoothstep(0., .045, abs(uv.y - uRoll));
  s.x += band * (.012 + .012 * hash(vec2(row, floor(uTime * 60.))));

  vec2 ab = vec2(uAberr, 0.);
  vec3 col = vec3(texture2D(uScene, s + ab).r, texture2D(uScene, s).g, texture2D(uScene, s - ab).b);
  vec3 glw = vec3(texture2D(uGlow, s + ab * 2.5).r, texture2D(uGlow, s).g, texture2D(uGlow, s - ab * 2.5).b);
  col += glw * uBloom;
  col *= step(0., s.x) * step(s.x, 1.) * step(0., s.y) * step(s.y, 1.);
  col *= 1. + (1. - uOpen) * 1.6;
  col += band * .10 * hash(vec2(floor(uv.x * 500.), row + floor(uTime * 60.)));

  float sn = hash(floor(uv * vec2(400., 300.)) + fract(uTime * 13.) * 97.);
  col = mix(col, vec3(sn), uStatic);

  vec2 d = (uv - .5) * vec2(aspect, 1.);
  float dx = max(abs(d.x) - .5 * uOpenX * aspect, 0.);
  float r2 = dx * dx + d.y * d.y;
  col += uBeam * (exp(-r2 / .00005) + .35 * exp(-r2 / .0025)) * vec3(.92, .96, 1.);
  col += uFlash;

  col *= .7 + .3 * (.5 + .5 * cos(gl_FragCoord.y * 6.28318 / uScanP));
  float m = mod(gl_FragCoord.x, 3.);
  col *= (m < 1. ? vec3(1., .74, .74) : (m < 2. ? vec3(.74, 1., .74) : vec3(.74, .74, 1.))) * 1.22;

  col += (hash(gl_FragCoord.xy + fract(uTime * 7.) * 100.) - .5) * .07;
  col = max(col, 0.);
  col = col * 1.3 / (1. + col * .3);
  col += vec3(.010, .010, .016);
  col *= .985 + .015 * sin(uTime * 377.);
  col *= clamp(1. - .3 * dot(c, c), 0., 1.);
  col += .035 * smoothstep(.75, 0., length(c - vec2(-.55, .62)));
  gl_FragColor = vec4(min(col, 1.) * mask, 1.);
}`;

const uniformNames = ["uScene", "uGlow", "uRes", "uTime", "uAberr", "uBloom", "uOpen", "uOpenX", "uBeam", "uFlash", "uNoise", "uRoll", "uStatic", "uScanP"] as const;

export function createRenderer(canvas: HTMLCanvasElement): Renderer | null {
  return createCrtRenderer(canvas) ?? createPlainRenderer(canvas);
}

function createPlainRenderer(canvas: HTMLCanvasElement): Renderer | null {
  const context = canvas.getContext("2d");
  if (!context) return null;
  return {
    lost: false,
    render(scene) {
      context.fillStyle = "#000";
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.drawImage(scene, 0, 0, canvas.width, canvas.height);
    },
  };
}

function createCrtRenderer(canvas: HTMLCanvasElement): Renderer | null {
  const gl = canvas.getContext("webgl", { alpha: false, antialias: false, premultipliedAlpha: false });
  if (!gl) return null;
  const program = buildProgram(gl);
  if (!program) return null;

  gl.useProgram(program);
  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const position = gl.getAttribLocation(program, "p");
  gl.enableVertexAttribArray(position);
  gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);

  const u = Object.fromEntries(uniformNames.map((name) => [name, gl.getUniformLocation(program, name)])) as Record<(typeof uniformNames)[number], WebGLUniformLocation | null>;
  const sceneTexture = createTexture(gl, 0);
  const glowTexture = createTexture(gl, 1);
  gl.uniform1i(u.uScene, 0);
  gl.uniform1i(u.uGlow, 1);
  gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, true);

  let lost = false;
  canvas.addEventListener("webglcontextlost", () => { lost = true; }, { once: true });

  return {
    get lost() { return lost; },
    render(scene, glow, params, now) {
      if (lost) return;
      gl.viewport(0, 0, canvas.width, canvas.height);
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, sceneTexture);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, scene);
      gl.activeTexture(gl.TEXTURE1);
      gl.bindTexture(gl.TEXTURE_2D, glowTexture);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, glow);
      gl.uniform2f(u.uRes, canvas.width, canvas.height);
      gl.uniform1f(u.uTime, now);
      gl.uniform1f(u.uAberr, params.aberr / scene.width);
      gl.uniform1f(u.uBloom, params.bloom);
      gl.uniform1f(u.uOpen, params.open);
      gl.uniform1f(u.uOpenX, params.openX);
      gl.uniform1f(u.uBeam, params.beam);
      gl.uniform1f(u.uFlash, params.flash);
      gl.uniform1f(u.uNoise, params.noise);
      gl.uniform1f(u.uRoll, params.roll);
      gl.uniform1f(u.uStatic, params.stat);
      gl.uniform1f(u.uScanP, Math.max(3, Math.round(canvas.height / 300)));
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    },
  };
}

function buildProgram(gl: WebGLRenderingContext): WebGLProgram | null {
  const compile = (type: number, source: string) => {
    const shader = gl.createShader(type);
    if (!shader) return null;
    gl.shaderSource(shader, source);
    gl.compileShader(shader);
    return gl.getShaderParameter(shader, gl.COMPILE_STATUS) ? shader : null;
  };
  const vertex = compile(gl.VERTEX_SHADER, vertexSource);
  const fragment = compile(gl.FRAGMENT_SHADER, fragmentSource);
  const program = gl.createProgram();
  if (!vertex || !fragment || !program) return null;
  gl.attachShader(program, vertex);
  gl.attachShader(program, fragment);
  gl.linkProgram(program);
  return gl.getProgramParameter(program, gl.LINK_STATUS) ? program : null;
}

function createTexture(gl: WebGLRenderingContext, unit: number): WebGLTexture | null {
  const texture = gl.createTexture();
  gl.activeTexture(gl.TEXTURE0 + unit);
  gl.bindTexture(gl.TEXTURE_2D, texture);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
  return texture;
}
