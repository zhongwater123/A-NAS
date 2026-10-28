import { describe, expect, it } from "vitest";

import { APIError } from "../api/client";
import { nameErrorText, nameNotPortableText, nameProblem, nameTooLongText } from "./fileNames";

describe("file names", () => {
  it("follow the same rules as the NAS", () => {
    expect(nameProblem("长".repeat(85))).toBeUndefined();
    expect(nameProblem("长".repeat(86))).toBe(nameTooLongText);
    for (const name of ["a:b", "what?", "a\tb", "CON", "con.txt", "Lpt1", "ends.", "ends ", "<x>", "a|b", "a/b"]) {
      expect(nameProblem(name), name).toBe(nameNotPortableText);
    }
    for (const name of ["CONSOLE", "con-notes", "a.b.c", " leading", "照片 😀", "COM0"]) {
      expect(nameProblem(name), name).toBeUndefined();
    }
    expect(nameProblem(" ")).toBe("请输入名称");
    expect(nameProblem(".a-nas-trash")).toMatch(/留给 A-NAS/);
  });

  it("word the NAS's name errors", () => {
    expect(nameErrorText(new APIError(422, "name_too_long", "names may hold at most 255 bytes"))).toBe(nameTooLongText);
    expect(nameErrorText(new APIError(422, "name_not_portable", "x"))).toBe(nameNotPortableText);
    expect(nameErrorText(new APIError(409, "name_conflict", "x"))).toMatch(/不区分大小写/);
    expect(nameErrorText(new Error("other"))).toBeUndefined();
  });
});
