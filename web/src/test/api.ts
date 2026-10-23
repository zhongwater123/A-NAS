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

async function bodyOf(request: Request): Promise<BodyInit | undefined> {
  if (!request.body) return undefined;
  const type = request.headers.get("Content-Type") ?? "";
  if (type.startsWith("multipart/form-data")) return request.formData();
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
