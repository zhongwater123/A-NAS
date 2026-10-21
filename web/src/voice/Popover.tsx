import { ReactNode, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

interface Props {
  label: string;
  trigger: ReactNode;
  className?: string;
  align?: "start" | "end";
  children: (close: () => void) => ReactNode;
  onOpenChange?: (open: boolean) => void;
}

// Popover is a button that opens a panel below it; a press outside or Escape
// closes it. The panel flips to stay inside the window.
export function Popover({ label, trigger, className = "", align = "end", children, onOpenChange }: Props) {
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLSpanElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const [placement, setPlacement] = useState<{ up: boolean; shift: number }>({ up: false, shift: 0 });
  const told = useRef(onOpenChange);
  told.current = onOpenChange;
  const change = useCallback((next: boolean) => { setOpen(next); told.current?.(next); }, []);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => { if (!wrapper.current?.contains(event.target as Node)) change(false); };
    const escape = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") { event.stopPropagation(); change(false); } };
    window.addEventListener("pointerdown", outside, true);
    window.addEventListener("keydown", escape, true);
    return () => { window.removeEventListener("pointerdown", outside, true); window.removeEventListener("keydown", escape, true); };
  }, [open, change]);
  useLayoutEffect(() => {
    const element = panel.current;
    const frame = element?.closest(".vo-app");
    if (!open || !element || !frame) return;
    const box = element.getBoundingClientRect();
    const bounds = frame.getBoundingClientRect();
    const up = box.bottom > bounds.bottom - 8 && box.height < box.top - bounds.top;
    let shift = 0;
    if (box.left < bounds.left + 8) shift = bounds.left + 8 - box.left;
    if (box.right > bounds.right - 8) shift = bounds.right - 8 - box.right;
    setPlacement({ up, shift });
  }, [open]);
  return (
    <span ref={wrapper} className={`vo-popover${open ? " open" : ""}`}>
      <button type="button" className={className} aria-label={label} title={label} aria-haspopup="menu" aria-expanded={open} onClick={() => change(!open)}>{trigger}</button>
      {open && (
        <div ref={panel} className={`vo-popover-panel ${align}${placement.up ? " up" : ""}`} style={{ translate: `${placement.shift}px 0` }}>
          {children(() => change(false))}
        </div>
      )}
    </span>
  );
}
