import createClient from "openapi-fetch";

import type { paths, Session } from "../api.generated";
import { responseValidators } from "../api.validators.generated";

let sessionEndedListener: (() => void) | undefined;

// onSessionEnded lets the signed-in desktop return to the login screen; it
// returns the unsubscribe function.
export function onSessionEnded(listener: () => void): () => void {
  sessionEndedListener = listener;
  return () => {
    if (sessionEndedListener === listener) sessionEndedListener = undefined;
  };
}

// sessionEnded reports a 401 that arrived outside apiFetch, such as on an
// upload part that tus-js-client sent.
export function sessionEnded() {
  sessionEndedListener?.();
}

// A write gate holds every write the page sends (any method but GET, HEAD and
// OPTIONS) until it says whether the write may go: VNAS's controller asks the
// member about the writes its steps on the desktop cause (assistant/webosControl.ts).
export type WriteGate = (method: string, path: string) => Promise<boolean>;
let writeGate: WriteGate | undefined;
export function setWriteGate(gate: WriteGate): () => void {
  writeGate = gate;
  return () => {
    if (writeGate === gate) writeGate = undefined;
  };
}

// passWriteGate waits for the write gate, and throws when the write may not go;
// requests sent outside apiFetch (tus-js-client's upload creation) call it too.
export async function passWriteGate(method: string, url: string) {
  const verb = method.toUpperCase();
  if (!writeGate || verb === "GET" || verb === "HEAD" || verb === "OPTIONS") return;
  if (!(await writeGate(verb, new URL(url, window.location.href).pathname))) {
    throw new APIError(403, "not_allowed_for_vnas", "没有允许 VNAS 这一步，什么都没有改");
  }
}

// apiFetch is fetch for product API calls. Once signed in, every 401 means the
// session ended (expired, signed out elsewhere or the account was disabled);
// wrong passwords are 401 only at login, before any listener exists.
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const given = input instanceof Request ? input : undefined;
  await passWriteGate(init?.method ?? given?.method ?? "GET", given?.url ?? String(input));
  let response: Response;
  try {
    response = await fetch(input, init);
  } catch (error) {
    if (init?.signal?.aborted || given?.signal.aborted) throw error;
    throw normalizeAPIError(error);
  }
  if (response.status === 401) sessionEndedListener?.();
  return response;
}

export class APIError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message = code,
  ) {
    super(message);
  }
}

// Cancellation remains recognizable to callers; unexpected response parsing
// failures are distinct from transport failures and never shown verbatim.
export function normalizeAPIError(error: unknown): APIError {
  if (error instanceof APIError) return error;
  if (error instanceof DOMException && error.name === "AbortError") throw error;
  return new APIError(
    0,
    error instanceof TypeError
      ? "network_error"
      : error instanceof SyntaxError
        ? "malformed_response"
        : "request_failed",
  );
}

let csrfToken = "";
export function setSession(session?: Session) {
  csrfToken = session?.csrfToken ?? "";
}
// csrfHeader is the header that carries the session's CSRF token on a request
// of this method: the server checks it on every method but GET and HEAD. The
// typed client, uploads with progress and tus all take it from here.
export function csrfHeader(method: string): Record<string, string> {
  const verb = method.toUpperCase();
  return csrfToken && verb !== "GET" && verb !== "HEAD" ? { "X-CSRF-Token": csrfToken } : {};
}

// The contract requires X-CSRF-Token of every write. The client sends it
// itself, so calls leave it out of their header parameters.
type WithoutCSRF<Operation> = Operation extends { parameters: { header: infer Header } }
  ? Omit<Operation, "parameters"> & {
      parameters: Omit<Operation["parameters"], "header"> & { header?: Omit<Header, "X-CSRF-Token"> };
    }
  : Operation;
type ClientPaths = { [Path in keyof paths]: { [Method in keyof paths[Path]]: WithoutCSRF<paths[Path][Method]> } };

// client calls the product API with the types generated from its contract
// (api.generated.ts, `npm run generate`). It sends what apiFetch sends, with
// the CSRF token on writes; call turns its result into data or an APIError.
// The page's origin is the base because a Request needs an absolute URL.
export const client = createClient<ClientPaths>({
  baseUrl: window.location.origin,
  headers: { Accept: "application/json" },
  fetch: (request) => apiFetch(request),
});
client.use({
  onRequest({ request }) {
    for (const [name, value] of Object.entries(csrfHeader(request.method))) request.headers.set(name, value);
    return request;
  },
  // The responses the desktop shell depends on are checked against the
  // contract before anyone reads them, so that a server answering null for a
  // list (10-07) fails the call instead of blanking the desktop. The list is
  // in tools/openapi/validators.mjs.
  async onResponse({ request, response, schemaPath }) {
    const validate = responseValidators[`${request.method} ${schemaPath} ${response.status}`];
    if (!validate) return undefined;
    const body: unknown = await response
      .clone()
      .json()
      .catch(() => undefined);
    if (validate(body)) return undefined;
    console.warn(`${request.method} ${schemaPath}: the response breaks the contract`, validate.errors);
    throw new APIError(response.status, "malformed_response", "服务返回的内容不完整，请稍后重试");
  },
});

// call waits for a client call and returns its data; a failure throws an
// APIError with the code and message of the server's error body.
export async function call<Data>(pending: Promise<{ data?: Data; error?: unknown; response: Response }>) {
  const { data, error, response } = await pending.catch((error: unknown) => {
    throw normalizeAPIError(error);
  });
  if (!response.ok) throw failure(response.status, error);
  return data as Data;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export function failure(status: number, body: unknown): APIError {
  const detail = isRecord(body) && isRecord(body.error) ? body.error : {};
  return new APIError(
    status,
    typeof detail.code === "string" ? detail.code : "request_failed",
    typeof detail.message === "string" ? detail.message : `请求失败 (${status})`,
  );
}

// uploadForm posts a multipart form as the client would, reporting upload
// progress as a fraction; fetch cannot report it.
export async function uploadForm<T>(
  path: string,
  body: FormData,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<T> {
  await passWriteGate("POST", path);
  signal?.throwIfAborted();
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const abort = () => xhr.abort();
    const finish = () => signal?.removeEventListener("abort", abort);
    xhr.open("POST", path);
    xhr.setRequestHeader("Accept", "application/json");
    for (const [name, value] of Object.entries(csrfHeader("POST"))) xhr.setRequestHeader(name, value);
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && event.total > 0) onProgress(event.loaded / event.total);
    };
    xhr.onload = () => {
      finish();
      if (xhr.status === 401) sessionEnded();
      let data: unknown;
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
      } catch {
        if (xhr.status >= 200 && xhr.status < 300) {
          reject(new APIError(xhr.status, "malformed_response"));
          return;
        }
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data as T);
      else reject(failure(xhr.status, data));
    };
    xhr.onerror = () => {
      finish();
      reject(new APIError(0, "network_error"));
    };
    xhr.ontimeout = xhr.onerror;
    xhr.onabort = () => {
      finish();
      reject(new DOMException("upload aborted", "AbortError"));
    };
    signal?.addEventListener("abort", abort, { once: true });
    xhr.send(body);
  });
}

// rawBody sends a Blob or a FormData as it is. The contract types binary
// content as a string, which a Blob is not, so the typed body is set aside.
export function rawBody(content: Blob | FormData) {
  return { body: content as never, bodySerializer: () => content };
}
