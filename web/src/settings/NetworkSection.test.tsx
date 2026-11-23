import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { afterEach, expect, it } from "vitest";

import { setSession } from "../api/client";
import type { PhoneHotspotStatus } from "../api.generated";
import { session } from "../test/fixtures";
import { http, server } from "../test/server";
import { NetworkSection } from "./NetworkSection";

afterEach(() => setSession());

it("opens Network without a parent query provider and controls the phone hotspot", async () => {
  const path = "/api/v1/network/phone-hotspot";
  const off: PhoneHotspotStatus = { state: "off" };
  const on: PhoneHotspotStatus = {
    state: "on",
    connection: { ssid: "A-NAS-3EB129", password: "preview-hotspot-only", url: "https://hotspot.example.test:8443/" },
  };
  let status = off;
  server.use(
    http.get(path, () => HttpResponse.json(status)),
    http.put(path, async ({ request }) => {
      status = (await request.json()).enabled ? on : off;
      return HttpResponse.json(status);
    }),
  );
  setSession(session());
  const user = userEvent.setup();
  render(<NetworkSection />);

  const toggle = screen.getByRole("switch", { name: "手机直连热点" });
  expect(await screen.findByText("已关闭")).toBeInTheDocument();
  expect(toggle).not.toBeChecked();
  await user.click(toggle);
  expect(await screen.findByText("https://hotspot.example.test:8443/")).toBeInTheDocument();
  expect(toggle).toBeChecked();
  expect(screen.queryByText("preview-hotspot-only")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "显示密码" }));
  expect(screen.getByText("preview-hotspot-only")).toBeInTheDocument();
  await waitFor(() => expect(toggle).toBeEnabled());
  await user.click(toggle);
  expect(await screen.findByText("已关闭")).toBeInTheDocument();
  expect(screen.queryByText("preview-hotspot-only")).not.toBeInTheDocument();
  expect(screen.queryByText("https://hotspot.example.test:8443/")).not.toBeInTheDocument();
});
