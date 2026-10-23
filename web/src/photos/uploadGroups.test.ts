import { describe, expect, it } from "vitest";
import { groupPhotoUploads } from "./uploadGroups";
const file = (name: string) => new File(["data"], name);
describe("Live Photo selection", () => {
  it("groups a matching movie first or last into one asset", () => {
    const photo = file("IMG_1.HEIC"),
      movie = file("img_1.MOV");
    expect(groupPhotoUploads([movie, photo])).toEqual([{ file: photo, motion: movie }]);
  });
  it("keeps Android single files and still photos independent", () => {
    expect(groupPhotoUploads([file("android.jpg"), file("still.png")])).toHaveLength(2);
  });
  it("sends a movie on its own as a video, which may complete a still uploaded earlier", () => {
    const movie = file("a.mov"),
      photo = file("b.jpg");
    expect(groupPhotoUploads([movie, photo])).toEqual([{ file: photo }, { file: movie, video: true }]);
  });
  it("keeps other videos apart from photos of the same name", () => {
    const clip = file("IMG_2.MP4"),
      photo = file("IMG_2.JPG"),
      typed = new File(["data"], "clip", { type: "video/webm" });
    expect(groupPhotoUploads([clip, photo, typed])).toEqual([
      { file: photo },
      { file: clip, video: true },
      { file: typed, video: true },
    ]);
  });
  it("rejects ambiguous basenames without losing a file", () => {
    const groups = groupPhotoUploads([file("a.jpg"), file("a.heic"), file("a.mov")]);
    expect(groups).toHaveLength(3);
    expect(groups.every((group) => group.error)).toBe(true);
  });
  it("does not pair files from different source folders", () => {
    const a = file("a.jpg"),
      b = file("a.mov");
    Object.defineProperty(a, "webkitRelativePath", { value: "one/a.jpg" });
    Object.defineProperty(b, "webkitRelativePath", { value: "two/a.mov" });
    expect(groupPhotoUploads([a, b])[0].motion).toBeUndefined();
  });
});
