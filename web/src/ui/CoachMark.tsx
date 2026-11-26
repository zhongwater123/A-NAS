// CoachMark is one step of a guide: a few words beside the element they are
// about, with the step's count and actions. It stays open until the guide
// moves on, and leaves the focus where it is, so that the keys the words
// talk about keep working; clicks elsewhere do not close it.
import { Popover } from "radix-ui";
import type { ReactNode, RefObject } from "react";

// The words sit this far into the anchor's top corner when they go inside it.
const inset = 20;

export function CoachMark({
  container,
  anchor,
  inside,
  side = "bottom",
  label,
  className,
  count,
  children,
  actions,
}: {
  // container holds the words, and they stay within it.
  container: RefObject<HTMLElement | null>;
  // anchor selects the element in the container the words are about, found
  // whenever they are placed; inside sets them in its top corner rather than
  // beside it, for an element as large as a photo.
  anchor: string;
  inside?: boolean;
  // side is where the words go beside the anchor when there is room there.
  side?: "top" | "bottom";
  label: string;
  className?: string;
  count?: string;
  children: ReactNode;
  actions: ReactNode;
}) {
  const find = () => container.current?.querySelector(anchor) ?? undefined;
  const reference = {
    current: {
      getBoundingClientRect: (): DOMRect => {
        const box = find()?.getBoundingClientRect();
        if (box && !inside) return box;
        const x = (box?.left ?? 0) + inset;
        const y = (box?.top ?? 0) + inset;
        return { x, y, left: x, top: y, right: x, bottom: y, width: 0, height: 0, toJSON: () => ({ x, y }) };
      },
      // The words follow the anchor as it moves or changes size.
      get contextElement() {
        return find();
      },
    },
  };
  return (
    <Popover.Root open>
      <Popover.Anchor virtualRef={reference} />
      <Popover.Portal container={container.current}>
        <Popover.Content
          className={className}
          aria-label={label}
          side={inside ? "bottom" : side}
          align={inside ? "start" : "center"}
          sideOffset={inside ? 0 : 12}
          collisionBoundary={container.current}
          collisionPadding={12}
          onOpenAutoFocus={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => event.preventDefault()}
          onInteractOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => event.preventDefault()}
        >
          <p aria-live="polite">{children}</p>
          <footer>
            {count && <span>{count}</span>}
            {actions}
          </footer>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
