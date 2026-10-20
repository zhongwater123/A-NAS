import { describe, expect, it } from "vitest";

import { wallpaperBackdrop, wallpaperBackground } from "./wallpaper";

const picture = { version: "0123456789abcdef", width: 400, height: 300 };

describe("wallpaper styles", () => {
  it("draws each style the way its name says", () => {
    const url = 'url("/api/v1/wallpaper/image?v=0123456789abcdef")';
    expect(wallpaperBackground({ style: "fill", image: picture })).toEqual({ backgroundColor: wallpaperBackdrop, backgroundImage: url, backgroundSize: "cover", backgroundRepeat: "no-repeat", backgroundPosition: "center" });
    expect(wallpaperBackground({ style: "fit", image: picture })).toMatchObject({ backgroundSize: "contain", backgroundRepeat: "no-repeat" });
    expect(wallpaperBackground({ style: "stretch", image: picture })).toMatchObject({ backgroundSize: "100% 100%", backgroundRepeat: "no-repeat" });
    expect(wallpaperBackground({ style: "center", image: picture })).toMatchObject({ backgroundSize: "400px 300px", backgroundRepeat: "no-repeat", backgroundPosition: "center" });
    expect(wallpaperBackground({ style: "tile", image: picture })).toMatchObject({ backgroundSize: "400px 300px", backgroundRepeat: "repeat", backgroundPosition: "left top" });
  });

  it("keeps the picture in proportion in a smaller preview", () => {
    expect(wallpaperBackground({ style: "tile", image: picture }, 0.25)).toMatchObject({ backgroundSize: "100px 75px" });
  });

  it("draws the built-in wallpaper in the chosen style and nothing before the first read", () => {
    const builtIn = wallpaperBackground({ style: "center" });
    expect(builtIn.backgroundImage).toMatch(/a-nas-wallpaper/);
    expect(builtIn.backgroundSize).toBe("1448px 1086px");
    expect(wallpaperBackground(undefined)).toEqual({ backgroundColor: wallpaperBackdrop, backgroundImage: "none" });
  });
});
