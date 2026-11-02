import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { setSession } from "../api/client";
import { session } from "../test/fixtures";
import { webSearchStatus } from "../test/handlers/websearch";
import { http, server } from "../test/server";
import { resetWebSearch, useWebSearch } from "./status";

function Reader({ name }: { name: string }) {
  const status = useWebSearch();
  return <p>{status ? `${name}: ${status.enabled ? "on" : "off"}` : `${name}: …`}</p>;
}

describe("the web search switch on the desktop", () => {
  beforeEach(() => setSession(session()));
  afterEach(() => {
    setSession();
    resetWebSearch();
  });

  it("is read once however many components show it", async () => {
    let reads = 0;
    server.use(
      http.get("/api/v1/web-search", ({ response }) => {
        reads += 1;
        return response(200).json(webSearchStatus(true));
      }),
    );
    const { unmount } = render(
      <>
        <Reader name="desktop" />
        <Reader name="window" />
        <Reader name="settings" />
      </>,
    );
    expect(await screen.findByText("desktop: on")).toBeInTheDocument();
    expect(screen.getByText("window: on")).toBeInTheDocument();
    expect(screen.getByText("settings: on")).toBeInTheDocument();
    expect(reads).toBe(1);

    // The last one to go stops the reads; the next one starts them again.
    unmount();
    render(<Reader name="desktop" />);
    await screen.findByText("desktop: on");
    expect(reads).toBe(2);
  });
});
