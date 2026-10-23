// Helpers for tests that answer the product API with a stubbed fetch.
import { vi } from "vitest";

type Answer = (input: string, init?: RequestInit) => Response | Promise<Response>;

// stubAPI makes fetch answer the product API through answer, which sees every
// call as a path (with its query) and an init, the way calls used to pass them
// to fetch: the typed client sends a Request, and its body is read back as
// text, a FormData or a Blob. The answer's calls are what a test inspects.
export function stubAPI<A extends Answer>(answer: A): A {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    if (!(input instanceof Request)) return answer(String(input), init);
    const url = new URL(input.url);
    return answer(url.pathname + url.search, {
      method: input.method,
      headers: input.headers,
      body: await bodyOf(input),
      signal: input.signal,
    });
  });
  return answer;
}

// Requests remember the FormData or Blob they were built with: Vitest's jsdom
// environment turns such a body into Node's on the way into a Request, and
// loses the files' bytes and names (Vitest 5.0, jsdom 30.1). setup.ts calls
// this before any module takes Request, as the typed client does.
const builtWith = new WeakMap<Request, FormData | Blob>();
export function rememberRequestBodies() {
  const Base = globalThis.Request;
  globalThis.Request = class extends Base {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init);
      if (init?.body instanceof FormData || init?.body instanceof Blob) builtWith.set(this, init.body);
    }
  };
}

async function bodyOf(request: Request): Promise<BodyInit | undefined> {
  const built = builtWith.get(request);
  if (built) return built;
  if (!request.body) return undefined;
  const type = request.headers.get("Content-Type") ?? "";
  if (type.startsWith("application/json") || type.startsWith("text/")) return request.text();
  return request.blob();
}

// json answers with body as JSON; 204 has no body.
export function json(body: unknown, status = 200): Response {
  return status === 204 ? new Response(null, { status }) : Response.json(body, { status });
}

// apiError answers with the product API's error body.
export function apiError(status: number, code: string, message = code): Response {
  return Response.json({ error: { code, message } }, { status });
}
