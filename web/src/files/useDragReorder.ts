import { DragEvent as ReactDragEvent, useRef, useState } from "react";

import { carriesFiles } from "../transfers/picker";
import { FileOrder, isPinned, reorderFiles } from "../useFileOrder";
import { FileBrowserItem } from "./model";

// useDragReorder lets items be dragged into a manual order. An item moves
// only among its peers, pinned or not, and only within the folder it was
// picked up in; files dragged in from outside are uploads, not moves.
export function useDragReorder({
  items,
  order,
  scope,
  enabled,
  save,
}: {
  items: FileBrowserItem[];
  order: FileOrder;
  // The folder's order key.
  scope: string;
  enabled: boolean;
  save: (order: FileOrder) => void;
}) {
  const draggedItem = useRef<{ id: string; scope: string } | undefined>(undefined);
  const [dropTarget, setDropTarget] = useState("");
  return (item: FileBrowserItem) => ({
    draggable: enabled,
    onDragStart: (event: ReactDragEvent<HTMLElement>) => {
      if (!enabled) {
        event.preventDefault();
        return;
      }
      draggedItem.current = { id: item.id, scope };
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", item.id);
    },
    onDragOver: (event: ReactDragEvent<HTMLElement>) => {
      const source = items.find((candidate) => candidate.id === draggedItem.current?.id);
      if (
        !enabled ||
        draggedItem.current?.scope !== scope ||
        !source ||
        source.id === item.id ||
        isPinned(source, order) !== isPinned(item, order)
      )
        return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      setDropTarget(item.id);
    },
    onDrop: (event: ReactDragEvent<HTMLElement>) => {
      if (carriesFiles(event.dataTransfer)) return;
      event.preventDefault();
      if (enabled && draggedItem.current?.scope === scope)
        save(reorderFiles(items, order, draggedItem.current.id, item.id));
      draggedItem.current = undefined;
      setDropTarget("");
    },
    onDragEnd: () => {
      draggedItem.current = undefined;
      setDropTarget("");
    },
    onDragLeave: () => setDropTarget(""),
    "data-drop-target": dropTarget === item.id || undefined,
  });
}

export type DragProps = ReturnType<ReturnType<typeof useDragReorder>>;
