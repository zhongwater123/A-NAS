import { describe, expect, it } from "vitest";

import { linkPhotos, photoOf } from "./photoLinks";

describe("photo links in the assistant's answers", () => {
  it("turns links to photo ids into ones the window recognises", () => {
    const text = "找到两张：[海边.jpg](photo:a1b2) 和 [日落.heic](photo:photo:c3d4)，还有[说明](https://example.com)。";
    const linked = linkPhotos(text);
    expect(linked).toBe("找到两张：[海边.jpg](#a-nas-photo/photo%3Aa1b2) 和 [日落.heic](#a-nas-photo/photo%3Ac3d4)，还有[说明](https://example.com)。");
    expect(photoOf("#a-nas-photo/photo%3Ac3d4")).toBe("photo:c3d4");
  });

  it("leaves everything else alone", () => {
    expect(linkPhotos("photo:a1b2 不在链接里")).toBe("photo:a1b2 不在链接里");
    expect(linkPhotos("[坏的](photo:../etc)")).toBe("[坏的](photo:../etc)");
    expect(photoOf("https://example.com")).toBeUndefined();
    expect(photoOf(undefined)).toBeUndefined();
  });
});
