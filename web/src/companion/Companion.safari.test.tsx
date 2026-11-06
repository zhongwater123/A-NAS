import { render, screen, waitFor } from "@testing-library/react";
import { HttpResponse } from "msw";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { http, server } from "../test/server";
import { Companion } from "./Companion";

// In a file of its own: dsh-pet settles the page's animation format once (media.ts).
const origin = window.location.origin;

beforeEach(() => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
    "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 Version/26.0 Mobile/15E148 Safari/604.1",
  );
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockReturnValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockReturnValue(undefined);
  URL.createObjectURL ??= () => "blob:animation";
  URL.revokeObjectURL ??= () => undefined;
});

describe("the companion in Safari", () => {
  it("plays dsh-pet's HEVC animations, which keep their transparency there", async () => {
    const fetched: string[] = [];
    server.use(
      http.untyped.get(`${origin}/companion/animations`, () =>
        HttpResponse.json({
          animations: {
            待机呼吸休闲: {
              webm: "/companion/thumb/main/%E5%BE%85%E6%9C%BA%E5%91%BC%E5%90%B8%E4%BC%91%E9%97%B2.webm",
              mov: "/companion/thumb/main/%E5%BE%85%E6%9C%BA%E5%91%BC%E5%90%B8%E4%BC%91%E9%97%B2.mov",
            },
          },
        }),
      ),
      http.untyped.get(`${origin}/companion/thumb/main/:name`, ({ request }) => {
        fetched.push(decodeURIComponent(new URL(request.url).pathname));
        return new HttpResponse(new Blob(["mov"]));
      }),
    );
    render(<Companion />);
    expect(await screen.findByLabelText("大肥鱼")).toBeTruthy();
    await waitFor(() => expect(fetched).toContain("/companion/thumb/main/待机呼吸休闲.mov"));
    expect(fetched.some((path) => path.endsWith(".webm"))).toBe(false);
  });
});
