import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { Dialog } from "./workspace";

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
