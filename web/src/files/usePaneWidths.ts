import {
  CSSProperties,
  KeyboardEvent as ReactKeyboardEvent,
  PointerEvent as ReactPointerEvent,
  RefObject,
  useCallback,
  useState,
} from "react";

type Side = "left" | "right";

// usePaneWidths sizes the folder tree and the details pane beside the file
// list, by dragging their separators or with the arrow keys; the list keeps
// room for itself.
export function usePaneWidths(managerRef: RefObject<HTMLDivElement | null>) {
  const [paneWidths, setPaneWidths] = useState({ left: 205, right: 238 });
  const setPaneWidth = useCallback(
    (side: Side, value: number) => {
      setPaneWidths((current) => {
        const total = managerRef.current?.clientWidth ?? 900;
        const opposite = side === "left" ? current.right : current.left;
        const minimum = side === "left" ? 145 : 175;
        const maximum = Math.max(minimum, total - opposite - 295);
        return { ...current, [side]: Math.round(Math.min(maximum, Math.max(minimum, value))) };
      });
    },
    [managerRef],
  );
  const beginPaneResize = (side: Side, event: ReactPointerEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.currentTarget.focus();
    const startX = event.clientX;
    const startWidth = paneWidths[side];
    const move = (moveEvent: PointerEvent) =>
      setPaneWidth(
        side,
        side === "left" ? startWidth + moveEvent.clientX - startX : startWidth - moveEvent.clientX + startX,
      );
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };
  const resizePaneWithKeyboard = (side: Side, event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const delta = event.key === "ArrowRight" ? 12 : -12;
    setPaneWidth(side, paneWidths[side] + (side === "left" ? delta : -delta));
  };
  return {
    style: {
      "--file-left-pane": `${paneWidths.left}px`,
      "--file-right-pane": `${paneWidths.right}px`,
    } as CSSProperties,
    resizer: (side: Side) => ({
      onPointerDown: (event: ReactPointerEvent<HTMLDivElement>) => beginPaneResize(side, event),
      onKeyDown: (event: ReactKeyboardEvent<HTMLDivElement>) => resizePaneWithKeyboard(side, event),
    }),
  };
}
