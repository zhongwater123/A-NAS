import { afterEach, describe, expect, it, vi } from "vitest";

import { createRenderer, type CrtParams } from "./crtRenderer";

afterEach(() => vi.restoreAllMocks());

const params: CrtParams = {
  open: 1,
  openX: 1,
  beam: 0,
  flash: 0,
  aberr: 0,
  bloom: 0,
  noise: 0,
  roll: -2,
  stat: 0,
};

function outputContext() {
  return {
    fillStyle: "",
    fillRect: vi.fn(),
    drawImage: vi.fn(),
  } as unknown as CanvasRenderingContext2D;
}

function shaderFailureContext() {
  return {
    VERTEX_SHADER: 1,
    FRAGMENT_SHADER: 2,
    COMPILE_STATUS: 3,
    createShader: vi.fn(() => ({})),
    shaderSource: vi.fn(),
    compileShader: vi.fn(),
    getShaderParameter: vi.fn(() => false),
    createProgram: vi.fn(() => ({})),
  } as unknown as WebGLRenderingContext;
}

function workingWebGLContext() {
  return {
    VERTEX_SHADER: 1,
    FRAGMENT_SHADER: 2,
    COMPILE_STATUS: 3,
    LINK_STATUS: 4,
    ARRAY_BUFFER: 5,
    STATIC_DRAW: 6,
    FLOAT: 7,
    TEXTURE0: 8,
    TEXTURE_2D: 9,
    TEXTURE_MIN_FILTER: 10,
    TEXTURE_MAG_FILTER: 11,
    LINEAR: 12,
    TEXTURE_WRAP_S: 13,
    TEXTURE_WRAP_T: 14,
    CLAMP_TO_EDGE: 15,
    TEXTURE1: 16,
    UNPACK_FLIP_Y_WEBGL: 17,
    createShader: vi.fn(() => ({})),
    shaderSource: vi.fn(),
    compileShader: vi.fn(),
    getShaderParameter: vi.fn(() => true),
    createProgram: vi.fn(() => ({})),
    attachShader: vi.fn(),
    linkProgram: vi.fn(),
    getProgramParameter: vi.fn(() => true),
    useProgram: vi.fn(),
    createBuffer: vi.fn(() => ({})),
    bindBuffer: vi.fn(),
    bufferData: vi.fn(),
    getAttribLocation: vi.fn(() => 0),
    enableVertexAttribArray: vi.fn(),
    vertexAttribPointer: vi.fn(),
    getUniformLocation: vi.fn(() => ({})),
    createTexture: vi.fn(() => ({})),
    activeTexture: vi.fn(),
    bindTexture: vi.fn(),
    texParameteri: vi.fn(),
    uniform1i: vi.fn(),
    pixelStorei: vi.fn(),
  } as unknown as WebGLRenderingContext;
}

function stubCanvases(output: HTMLCanvasElement, internal: HTMLCanvasElement, context: CanvasRenderingContext2D, gl: WebGLRenderingContext) {
  vi.spyOn(output, "getContext").mockImplementation(((kind: string) => kind === "2d" ? context : null) as never);
  vi.spyOn(internal, "getContext").mockImplementation(((kind: string) => kind === "webgl" ? gl : null) as never);
  const createElement = document.createElement.bind(document);
  vi.spyOn(document, "createElement").mockImplementation(((tag: string, options?: ElementCreationOptions) =>
    tag === "canvas" ? internal : createElement(tag, options)) as typeof document.createElement);
}

describe("CRT renderer", () => {
  it("falls back to plain 2D when shader compilation fails", () => {
    const output = document.createElement("canvas");
    const internal = document.createElement("canvas");
    const scene = document.createElement("canvas");
    const glow = document.createElement("canvas");
    const context = outputContext();
    stubCanvases(output, internal, context, shaderFailureContext());

    const renderer = createRenderer(output);
    expect(renderer).not.toBeNull();
    renderer!.render(scene, glow, params, 0);

    expect(context.drawImage).toHaveBeenCalledWith(scene, 0, 0, output.width, output.height);
  });

  it("reports a lost WebGL context so playback can hand over to the desktop", () => {
    const output = document.createElement("canvas");
    const internal = document.createElement("canvas");
    stubCanvases(output, internal, outputContext(), workingWebGLContext());

    const renderer = createRenderer(output);
    expect(renderer?.lost).toBe(false);
    internal.dispatchEvent(new Event("webglcontextlost"));
    expect(renderer?.lost).toBe(true);
  });
});
