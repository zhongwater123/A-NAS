// The stand-ins in the browser, for the Fake preview (.claude/skills/fake-preview),
// built only with VITE_VIDEOCUT_STANDINS=1. The page's fetch asks them first
// and goes to the server with what none of them answers. No service worker:
// the desktop serves only its own files, and none at the root.
import { getResponse } from "msw";
import { createOpenApiHttp } from "openapi-msw";

import type { paths } from "../../api.generated";
import { VenusFake } from "./fake";
import { videocutHandlers } from "./handlers";

export function startStandIns(): void {
  const http = createOpenApiHttp<paths>({ baseUrl: window.location.origin });
  const handlers = videocutHandlers(new VenusFake(), http);
  const serverFetch = window.fetch.bind(window);
  window.fetch = async (input, init) => {
    const request = new Request(input, init);
    return (await getResponse(handlers, request.clone())) ?? serverFetch(request);
  };
}
