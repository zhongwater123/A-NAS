import { Undo2 } from "lucide-react";

import { useEditor } from "../../app/editor";
import { ActionButton } from "../../ui/ActionButton";
import { messages } from "../../ui/messages";

// 撤销 (M6-5): one step back, the last edit only; grey when there is none.
// Ctrl+Z does the same (app/shortcuts.ts).
export function UndoButton({ workId }: { workId: string }) {
  const editor = useEditor(workId);
  return (
    <ActionButton disabled={!editor.canUndo} onClick={() => void editor.undo()}>
      <Undo2 size={15} aria-hidden="true" /> {messages.undo}
    </ActionButton>
  );
}
