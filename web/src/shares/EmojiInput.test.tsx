import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, it } from "vitest";

import { EmojiInput } from "./EmojiInput";

afterEach(() => localStorage.clear());
function Input() {
  const [value, setValue] = useState("你好世界");
  return <EmojiInput label="留言" value={value} onChange={setValue} />;
}
it("searches emoji and replaces the selected text without losing the rest of the draft", async () => {
  const user = userEvent.setup();
  render(<Input />);
  const input = screen.getByLabelText("留言") as HTMLTextAreaElement;
  input.focus();
  input.setSelectionRange(2, 4);
  fireEvent.select(input);
  await user.click(screen.getByRole("button", { name: "选择表情" }));
  await user.type(screen.getByRole("searchbox", { name: "搜索表情" }), "庆祝");
  await user.click(screen.getByRole("button", { name: "🎉" }));
  expect(input.value).toBe("你好🎉");
  expect(input.selectionStart).toBe(4);
  await user.click(screen.getByRole("button", { name: "关闭表情" }));
  await user.click(screen.getByRole("button", { name: "选择表情" }));
  await user.click(screen.getByRole("button", { name: "最近使用" }));
  expect(within(screen.getByRole("dialog", { name: "表情" })).getByRole("button", { name: "🎉" })).toBeTruthy();
});
it("offers categories and closes with Escape without dismissing the containing composer", async () => {
  const user = userEvent.setup();
  render(<Input />);
  await user.click(screen.getByRole("button", { name: "选择表情" }));
  await user.click(screen.getByRole("button", { name: "动物自然" }));
  expect(screen.getByRole("button", { name: "🐱" })).toBeTruthy();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "表情" })).toBeNull();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "选择表情" }));
});

it("does not submit the surrounding share form when Enter is pressed in emoji search", async () => {
  let submitted = 0;
  const user = userEvent.setup();
  render(
    <form
      onSubmit={(event) => {
        event.preventDefault();
        submitted++;
      }}
    >
      <Input />
      <button type="submit">发送</button>
    </form>,
  );
  await user.click(screen.getByRole("button", { name: "选择表情" }));
  await user.type(screen.getByRole("searchbox", { name: "搜索表情" }), "庆祝{Enter}");
  expect(submitted).toBe(0);
  expect(screen.getByRole("dialog", { name: "表情" })).toBeTruthy();
});
