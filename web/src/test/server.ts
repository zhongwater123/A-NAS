// The product API in tests: MSW answers every request a test makes. Handlers
// are written with http below, typed by the contract (api.generated.ts): a path
// the contract does not have, or a response it does not allow, fails type
// checking. A request no handler answers fails the test (setup.ts).
//
// Each test serves what it reads: the handler sets in handlers/ by domain, then
// server.use(...) for what the test changes. The first handler that matches
// answers: within one server.use call that is the earlier one, and a later
// server.use call goes before everything served so far.
import { setupServer } from "msw/node";
import { createOpenApiHttp } from "openapi-msw";

import type { ErrorResponse, paths } from "../api.generated";

export const server = setupServer();

// The client calls the page's own origin (api/client.ts).
export const http = createOpenApiHttp<paths>({ baseUrl: window.location.origin });

// apiError is the product API's error body.
export function apiError(code: string, message = code): ErrorResponse {
  return { error: { code, message } };
}

// pathOf is a request's path with its query, as the client wrote it.
export function pathOf(request: Request): string {
  const url = new URL(request.url);
  return url.pathname + url.search;
}

export { formOf } from "./forms";
