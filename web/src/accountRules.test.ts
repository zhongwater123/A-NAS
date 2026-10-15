import { describe, expect, it } from "vitest";

import { accountErrorText, nicknameProblem, passwordProblem, usernameProblem } from "./accountRules";
import { APIError } from "./api";

describe("account rules", () => {
  it("take the names the NAS takes", () => {
    for (const name of ["alice", " Carol ", "bob_2", "anas", "applepie", "a".repeat(32)]) {
      expect(usernameProblem(name), name).toBeUndefined();
    }
    for (const name of ["ab", "2bob", "-bob", "小明", "zhang san", "machine$", "a".repeat(33)]) {
      expect(usernameProblem(name), name).toMatch(/3–32 位/);
    }
    for (const name of ["Shared", "homes", "global", "printers", "a-nas", "a-nas-photos", "app-memos"]) {
      expect(usernameProblem(name), name).toMatch(/留给系统/);
    }
  });

  it("count password characters as the NAS does", () => {
    for (const password of ["twelve chars", "  spaces count too  ", "十二个汉字也可以作为一个密码", "x".repeat(64), "😀".repeat(12)]) {
      expect(passwordProblem(password), password).toBeUndefined();
    }
    expect(passwordProblem("eleven char")).toMatch(/至少/);
    expect(passwordProblem("😀".repeat(6))).toMatch(/至少/);
    expect(passwordProblem("x".repeat(65))).toMatch(/最多/);
    expect(passwordProblem("😀".repeat(64))).toMatch(/最多/);
    for (const password of ["short\nshort\nthen any text", "short\u0000then any text", "tab\tseparated text"]) {
      expect(passwordProblem(password), JSON.stringify(password)).toMatch(/控制字符/);
    }
  });

  it("check nicknames as the NAS does", () => {
    for (const nickname of ["小爱", "  Mom  ", "老 爸", "👩\u200d💻 程序员", "一".repeat(24), `${"一".repeat(24)}   `]) {
      expect(nicknameProblem(nickname), nickname).toBeUndefined();
    }
    expect(nicknameProblem("一".repeat(25))).toMatch(/最多 24/);
    for (const nickname of ["line\nbreak", "tab\there", "evil\u202ename", "zero\u200bwidth"]) {
      expect(nicknameProblem(nickname), JSON.stringify(nickname)).toMatch(/不可见/);
    }
    expect(accountErrorText(new APIError(409, "nickname_unavailable", "x"))).toMatch(/换一个/);
    expect(accountErrorText(new APIError(415, "unsupported_image", "x"))).toMatch(/JPEG/);
  });

  it("word the NAS's account errors", () => {
    expect(accountErrorText(new APIError(422, "invalid_username", "invalid username"))).toMatch(/3–32 位/);
    expect(accountErrorText(new APIError(409, "username_unavailable", "username is unavailable"))).toMatch(/换一个/);
    expect(accountErrorText(new APIError(422, "invalid_password", "invalid password"))).toMatch(/12–64/);
    expect(accountErrorText(new APIError(401, "invalid_credentials", "username or password is invalid"))).toBe("账号或密码不正确");
    expect(accountErrorText(new APIError(409, "conflict", "x"))).toBeUndefined();
    expect(accountErrorText(new Error("other"))).toBeUndefined();
  });
});
