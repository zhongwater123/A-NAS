import { describe, expect, it } from "vitest";

import { linkPhotos, photoOf, photosIn } from "./photoLinks";

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

  it("lists the photos an answer names, once each, for the pictures under it", () => {
    const answer = linkPhotos("找到 3 张钓鱼的照片：\n1. [**IMG_2031.JPG**](photo:p1)，2024-05-12\n2. [IMG_2032.JPG](photo:p2)\n3. [](photo:p3)\n4. [`_DSC0042.ARW`](photo:p4)\n再看一眼 [IMG_2031.JPG](photo:p1)。[说明](https://example.com)");
    expect(photosIn(answer)).toEqual([
      { id: "photo:p1", name: "IMG_2031.JPG" },
      { id: "photo:p2", name: "IMG_2032.JPG" },
      { id: "photo:p3", name: "照片" },
      { id: "photo:p4", name: "_DSC0042.ARW" },
    ]);
    expect(photosIn("没有照片")).toEqual([]);
  });
});
