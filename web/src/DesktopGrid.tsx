import { KeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode, useCallback, useLayoutEffect, useRef, useState } from "react";

import { moveItem, useDesktopOrder } from "./useDesktopOrder";

export interface DesktopApp {
  id: string;
  label: string;
  ariaLabel: string;
  tone: string;
  icon: ReactNode;
  onClick?: () => void;
  active?: boolean;
  disabled?: boolean;
}

interface Position {
  left: number;
  top: number;
}

interface DragState {
  id: string;
  pointerID: number;
  startX: number;
  startY: number;
  grabX: number;
  grabY: number;
  pointerX: number;
  pointerY: number;
  started: boolean;
  originalOrder: string[];
}

// Movement below this many pixels is a click, not a drag.
const dragThreshold = 6;
const keyboardSteps: Record<string, "previous" | "next" | "up" | "down"> = {
  ArrowLeft: "previous",
  ArrowRight: "next",
  ArrowUp: "up",
  ArrowDown: "down",
};

export function DesktopGrid({ apps }: { apps: DesktopApp[] }) {
  const { order, setOrder, save } = useDesktopOrder(apps.map((app) => app.id));
  const appsByID = new Map(apps.map((app) => [app.id, app]));
  const gridRef = useRef<HTMLElement>(null);
  const slots = useRef(new Map<string, HTMLDivElement>());
  const positions = useRef(new Map<string, Position>());
  const orderRef = useRef(order);
  const drag = useRef<DragState | null>(null);
  const suppressClick = useRef(false);
  const [draggingID, setDraggingID] = useState<string>();
  const [announcement, setAnnouncement] = useState("");
  orderRef.current = order;

  const placeDragged = useCallback(() => {
    const state = drag.current;
    const grid = gridRef.current;
    const slot = state && slots.current.get(state.id);
    if (!state?.started || !grid || !slot) return;
    // offsetLeft/offsetTop ignore transforms, so this is the slot's resting place.
    const gridBox = grid.getBoundingClientRect();
    const restX = gridBox.left + slot.offsetLeft - grid.scrollLeft;
    const restY = gridBox.top + slot.offsetTop - grid.scrollTop;
    slot.style.transform = `translate(${state.pointerX - state.grabX - restX}px, ${state.pointerY - state.grabY - restY}px)`;
  }, []);

  // Slide displaced icons from their previous cell (FLIP) and keep the dragged icon under the pointer.
  useLayoutEffect(() => {
    const next = new Map<string, Position>();
    slots.current.forEach((slot, id) => {
      const position = { left: slot.offsetLeft, top: slot.offsetTop };
      next.set(id, position);
      const previous = positions.current.get(id);
      const moved = previous && (previous.left !== position.left || previous.top !== position.top);
      if (moved && id !== drag.current?.id && typeof slot.animate === "function") {
        slot.animate(
          [{ transform: `translate(${previous.left - position.left}px, ${previous.top - position.top}px)` }, { transform: "none" }],
          { duration: 200, easing: "cubic-bezier(.2,.8,.2,1)" },
        );
      }
    });
    positions.current = next;
    placeDragged();
  }, [order, placeDragged]);

  const announceMove = (id: string, next: string[]) => {
    setAnnouncement(`已将${appsByID.get(id)?.label ?? ""}移动到第 ${next.indexOf(id) + 1} 位`);
  };

  const finishDrag = (cancelled: boolean) => {
    const state = drag.current;
    drag.current = null;
    if (!state?.started) return;
    const slot = slots.current.get(state.id);
    if (slot) slot.style.transform = "";
    setDraggingID(undefined);
    if (cancelled) {
      setOrder(state.originalOrder);
      return;
    }
    save(orderRef.current);
    announceMove(state.id, orderRef.current);
    // The pointerup that ends a drag is followed by a click; it must not open the app.
    suppressClick.current = true;
    window.setTimeout(() => {
      suppressClick.current = false;
    }, 0);
  };

  const beginPointer = (event: ReactPointerEvent<HTMLDivElement>, id: string) => {
    // Touch keeps native scrolling of the icon grid; mouse and pen can drag.
    if (event.button !== 0 || event.pointerType === "touch" || drag.current) return;
    drag.current = {
      id,
      pointerID: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      grabX: 0,
      grabY: 0,
      pointerX: event.clientX,
      pointerY: event.clientY,
      started: false,
      originalOrder: orderRef.current,
    };

    const move = (moveEvent: PointerEvent) => {
      const state = drag.current;
      const grid = gridRef.current;
      if (!state || moveEvent.pointerId !== state.pointerID || !grid) return;
      state.pointerX = moveEvent.clientX;
      state.pointerY = moveEvent.clientY;
      if (!state.started) {
        if (Math.hypot(moveEvent.clientX - state.startX, moveEvent.clientY - state.startY) < dragThreshold) return;
        const box = slots.current.get(state.id)?.getBoundingClientRect();
        if (!box) return;
        state.started = true;
        state.grabX = state.startX - box.left;
        state.grabY = state.startY - box.top;
        setDraggingID(state.id);
      }
      moveEvent.preventDefault();
      const target = nearestIndex(grid, orderRef.current.map((slotID) => slots.current.get(slotID)), moveEvent.clientX, moveEvent.clientY);
      if (target !== orderRef.current.indexOf(state.id)) {
        setOrder((current) => moveItem(current, state.id, target));
      } else {
        placeDragged();
      }
    };
    const stop = (cancelled: boolean) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", cancel);
      window.removeEventListener("keydown", escape);
      finishDrag(cancelled);
    };
    const up = (upEvent: PointerEvent) => upEvent.pointerId === drag.current?.pointerID && stop(false);
    const cancel = (cancelEvent: PointerEvent) => cancelEvent.pointerId === drag.current?.pointerID && stop(true);
    const escape = (keyEvent: globalThis.KeyboardEvent) => {
      if (keyEvent.key === "Escape") stop(true);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", cancel);
    window.addEventListener("keydown", escape);
  };

  const moveWithKeyboard = (event: KeyboardEvent<HTMLDivElement>, id: string) => {
    const step = keyboardSteps[event.key];
    if (!event.altKey || !step || !gridRef.current) return;
    event.preventDefault();
    const columns = Math.max(1, getComputedStyle(gridRef.current).gridTemplateColumns.split(" ").filter(Boolean).length);
    const offset = { previous: -1, next: 1, up: -columns, down: columns }[step];
    const next = moveItem(orderRef.current, id, orderRef.current.indexOf(id) + offset);
    setOrder(next);
    save(next);
    announceMove(id, next);
  };

  return (
    <section
      ref={gridRef}
      className={`desktop-grid ${draggingID ? "reordering" : ""}`}
      aria-label="桌面应用"
      aria-describedby="desktop-reorder-hint"
      onClickCapture={(event) => {
        if (!suppressClick.current) return;
        suppressClick.current = false;
        event.preventDefault();
        event.stopPropagation();
      }}
    >
      {order.map((id) => {
        const app = appsByID.get(id);
        if (!app) return null;
        return (
          <div
            key={id}
            ref={(slot) => {
              if (slot) slots.current.set(id, slot);
              else slots.current.delete(id);
            }}
            className={`desktop-slot ${draggingID === id ? "dragging" : ""}`}
            title={app.disabled ? `${app.label} · 规划中` : app.label}
            onPointerDown={(event) => beginPointer(event, id)}
            onKeyDown={(event) => moveWithKeyboard(event, id)}
          >
            <button
              className={`desktop-shortcut ${app.active ? "running" : ""}`}
              aria-label={app.ariaLabel}
              disabled={app.disabled}
              onClick={app.onClick}
            >
              <span className={`desktop-icon icon-${app.tone}`}>{app.icon}</span>
              <span>{app.label}</span>
              {app.disabled && <small>规划中</small>}
            </button>
          </div>
        );
      })}
      <p id="desktop-reorder-hint" className="sr-only">拖动图标，或按 Alt 加方向键，可以调整图标顺序</p>
      <p className="sr-only" aria-live="polite">{announcement}</p>
    </section>
  );
}

// Grid cells keep their positions while items move between them, so the
// nearest cell centre to the pointer is the drop index.
function nearestIndex(grid: HTMLElement, slots: (HTMLDivElement | undefined)[], clientX: number, clientY: number): number {
  const box = grid.getBoundingClientRect();
  const x = clientX - box.left + grid.scrollLeft;
  const y = clientY - box.top + grid.scrollTop;
  let best = 0;
  let bestDistance = Infinity;
  slots.forEach((slot, index) => {
    if (!slot) return;
    const distance = Math.hypot(slot.offsetLeft + slot.offsetWidth / 2 - x, slot.offsetTop + slot.offsetHeight / 2 - y);
    if (distance < bestDistance) {
      best = index;
      bestDistance = distance;
    }
  });
  return best;
}
