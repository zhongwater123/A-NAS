import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Dialog, useToasts } from "./workspace";

function Sheet({
  title,
  onClose,
  busy,
  children,
}: {
  title: string;
  onClose: () => void;
  busy?: boolean;
  children?: ReactNode;
}) {
  return (
    <Dialog
      title={title}
      icon={null}
      busy={busy}
      onClose={onClose}
      footer={
        <button type="button" data-autofocus>
          取消
        </button>
      }
    >
      {children}
    </Dialog>
  );
}

describe("Dialog", () => {
  it("closes on Escape only the dialog the focus is in, not one in another window", async () => {
    const user = userEvent.setup();
    const backup = vi.fn();
    const trash = vi.fn();
    render(
      <>
        <section aria-label="备份中心">
          <Sheet title="恢复" onClose={backup} />
        </section>
        <section aria-label="回收站">
          <Sheet title="彻底删除" onClose={trash} />
        </section>
      </>,
    );
    // The later dialog took the focus as it opened; move it to the first.
    await user.click(screen.getByRole("dialog", { name: "恢复" }));
    await user.keyboard("{Escape}");
    expect(backup).toHaveBeenCalledTimes(1);
    expect(trash).not.toHaveBeenCalled();
  });

  it("closes only the innermost of stacked dialogs, and none while busy", async () => {
    const user = userEvent.setup();
    const outer = vi.fn();
    const inner = vi.fn();
    const { rerender } = render(<Sheet title="历史版本" onClose={outer} />);
    // The inner one opens from the outer, and takes the focus as it does.
    rerender(
      <Sheet title="历史版本" onClose={outer}>
        <Sheet title="恢复这个版本" onClose={inner} />
      </Sheet>,
    );
    await user.keyboard("{Escape}");
    expect(inner).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();

    rerender(<Sheet title="历史版本" onClose={outer} busy />);
    await user.click(screen.getByRole("dialog", { name: "历史版本" }));
    await user.keyboard("{Escape}");
    expect(outer).not.toHaveBeenCalled();
  });
});

function Toaster({ tone }: { tone: "success" | "error" }) {
  const { toast, element } = useToasts();
  return (
    <>
      <button type="button" onClick={() => toast("已完成", tone)}>
        提示
      </button>
      {element}
    </>
  );
}

describe("useToasts", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("dismisses a toast after 4.5 s, and an error after 7 s", () => {
    vi.useFakeTimers();
    const { rerender } = render(<Toaster tone="success" />);
    act(() => screen.getByRole("button", { name: "提示" }).click());
    expect(screen.getByRole("status")).toHaveTextContent("已完成");
    act(() => vi.advanceTimersByTime(4499));
    expect(screen.queryByRole("status")).not.toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.queryByRole("status")).toBeNull();

    rerender(<Toaster tone="error" />);
    act(() => screen.getByRole("button", { name: "提示" }).click());
    act(() => vi.advanceTimersByTime(6999));
    expect(screen.getByRole("alert")).toHaveTextContent("已完成");
    act(() => vi.advanceTimersByTime(1));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
  });

  // A timer left after unmount fires once the test's jsdom is gone and
  // fails the run with "window is not defined" (PR #683's CI).
  it("clears its timers when the window closes", () => {
    vi.useFakeTimers();
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const { unmount } = render(<Toaster tone="error" />);
    act(() => screen.getByRole("button", { name: "提示" }).click());
    act(() => screen.getByRole("button", { name: "提示" }).click());
    unmount();
    expect(vi.getTimerCount()).toBe(0);
    expect(() => vi.advanceTimersByTime(7000)).not.toThrow();
    expect(errors).not.toHaveBeenCalled();
    errors.mockRestore();
  });
});
