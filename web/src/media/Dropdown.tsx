import { Check, ChevronDown } from "lucide-react";
import { KeyboardEvent, ReactNode, RefObject, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

// useFold keeps a popup mounted while it folds away after closing, so it
// leaves as smoothly as it came.
export function useFold(open: boolean, ms = 130): { shown: boolean; closing: boolean } {
  const [shown, setShown] = useState(open);
  if (open && !shown) setShown(true);
  useEffect(() => {
    if (open || !shown) return;
    const timer = window.setTimeout(() => setShown(false), ms);
    return () => window.clearTimeout(timer);
  }, [open, shown, ms]);
  return { shown, closing: shown && !open };
}

export interface Choice {
  value: string;
  label: string;
  hint?: string;
  icon?: ReactNode;
}

interface Place {
  root: Element;
  left: number;
  top: number;
  width: number;
  maxHeight: number;
  up: boolean;
}

// ChoiceList is the floating list of a dropdown. It sits against its anchor
// inside the media center, or inside the player, which may be fullscreen,
// and unfolds from the anchor's edge. Arrow keys move through it, Escape
// closes it, and so does a press or a scroll elsewhere.
export function ChoiceList({
  anchor: anchorRef,
  open,
  choices,
  value,
  label,
  align = "start",
  onPick,
  onClose,
}: {
  anchor: RefObject<HTMLElement | null>;
  open: boolean;
  choices: Choice[];
  value: string;
  label: string;
  align?: "start" | "end";
  onPick: (value: string) => void;
  onClose: (refocus: boolean) => void;
}) {
  const { shown, closing } = useFold(open);
  const [place, setPlace] = useState<Place>();
  const list = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!open || !anchorRef.current) return;
    const root = anchorRef.current.closest(".mc-player, .mc-app");
    if (!root) return;
    const frame = root.getBoundingClientRect();
    const box = anchorRef.current.getBoundingClientRect();
    const below = frame.bottom - box.bottom - 14;
    const above = box.top - frame.top - 14;
    const wanted = Math.min(choices.length * 36 + 12, 340);
    const up = below < wanted && above > below;
    setPlace({
      root,
      up,
      width: box.width,
      left: (align === "end" ? box.right : box.left) - frame.left,
      top: (up ? box.top - 6 : box.bottom + 6) - frame.top,
      maxHeight: Math.max(120, Math.min(340, up ? above : below)),
    });
  }, [open, anchorRef, choices.length, align]);
  // A list wider than its anchor stays inside the frame.
  useLayoutEffect(() => {
    const element = list.current;
    if (!place || !element) return;
    const frame = place.root.getBoundingClientRect();
    const box = element.getBoundingClientRect();
    const over = box.right - (frame.right - 8);
    const under = frame.left + 8 - box.left;
    element.style.marginLeft = over > 0 ? `${-over}px` : under > 0 ? `${under}px` : "";
  }, [place, shown]);
  const dismiss = useRef(onClose);
  useLayoutEffect(() => {
    dismiss.current = onClose;
  });
  useEffect(() => {
    if (!open || !place) return;
    const options = Array.from(list.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);
    (options.find((option) => option.getAttribute("aria-selected") === "true") ?? options[0])?.focus({
      preventScroll: true,
    });
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView?.({ block: "nearest" });
    const close = () => dismiss.current(false);
    const outside = (target: EventTarget | null) =>
      !list.current?.contains(target as Node) && !anchorRef.current?.contains(target as Node);
    const press = (event: PointerEvent) => {
      if (outside(event.target)) close();
    };
    const scroll = (event: Event) => {
      if (!list.current?.contains(event.target as Node)) close();
    };
    const resize = close;
    window.addEventListener("pointerdown", press, true);
    window.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", resize);
    return () => {
      window.removeEventListener("pointerdown", press, true);
      window.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", resize);
    };
  }, [open, place, anchorRef]);
  // Keys stay in the list, so the player does not also take them.
  const keys = (event: KeyboardEvent) => {
    if (event.key !== "Tab") event.stopPropagation();
    const options = Array.from(list.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);
    const index = options.indexOf(document.activeElement as HTMLButtonElement);
    const move = (to: number) => {
      event.preventDefault();
      options[(to + options.length) % options.length]?.focus({ preventScroll: false });
    };
    if (event.key === "ArrowDown") move(index + 1);
    else if (event.key === "ArrowUp") move(index - 1);
    else if (event.key === "Home") move(0);
    else if (event.key === "End") move(options.length - 1);
    else if (event.key === "Escape") {
      event.preventDefault();
      onClose(true);
    } else if (event.key === "Tab") onClose(false);
  };
  if (!shown || !place) return null;
  return createPortal(
    <div
      ref={list}
      role="listbox"
      aria-label={label}
      aria-hidden={closing || undefined}
      onKeyDown={keys}
      className={`mc-choices ${place.up ? "up" : ""} ${align === "end" ? "end" : ""} ${closing ? "closing" : ""}`}
      style={{ left: place.left, top: place.top, minWidth: place.width, maxHeight: place.maxHeight }}
    >
      {choices.map((choice) => {
        const selected = choice.value === value;
        return (
          <button
            key={choice.value}
            type="button"
            role="option"
            aria-selected={selected}
            tabIndex={closing ? -1 : 0}
            className={selected ? "selected" : ""}
            onClick={() => onPick(choice.value)}
          >
            {choice.icon}
            <span>{choice.label}</span>
            {choice.hint && <small>{choice.hint}</small>}
            <Check className="mc-choice-check" />
          </button>
        );
      })}
    </div>,
    place.root,
  );
}

// Dropdown picks one of a few choices, in place of a native select whose
// list the browser draws in its own style.
export function Dropdown({
  value,
  choices,
  onChange,
  label,
  icon,
  prefix,
  className = "",
  align,
}: {
  value: string;
  choices: Choice[];
  onChange: (value: string) => void;
  label: string;
  icon?: ReactNode;
  prefix?: string;
  className?: string;
  align?: "start" | "end";
}) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const current = choices.find((choice) => choice.value === value) ?? choices[0];
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) trigger.current?.focus({ preventScroll: true });
  };
  return (
    <>
      <button
        ref={trigger}
        type="button"
        className={`mc-dropdown ${open ? "open" : ""} ${className}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={`${label}：${current?.label ?? ""}`}
        onClick={() => setOpen(!open)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            event.stopPropagation();
            setOpen(true);
          }
        }}
      >
        {icon}
        {prefix && <span className="mc-dropdown-prefix">{prefix}</span>}
        <span className="mc-dropdown-value">{current?.label}</span>
        <ChevronDown className="mc-dropdown-chevron" />
      </button>
      <ChoiceList
        anchor={trigger}
        open={open}
        choices={choices}
        value={value}
        label={label}
        align={align}
        onPick={(next) => {
          close(true);
          if (next !== value) onChange(next);
        }}
        onClose={close}
      />
    </>
  );
}
