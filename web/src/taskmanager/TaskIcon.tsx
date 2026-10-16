import { Activity, Layers } from "lucide-react";
import { useState } from "react";
import type { Task } from "./api";

export function TaskIcon({ task, icons, processes }: { task: Task; icons?: Record<string, string>; processes: boolean }) {
  const source = task.iconId ? icons?.[task.iconId] : undefined;
  const [failed, setFailed] = useState<string>();
  const localImage = source && source.length <= 24576 && /^data:image\/(png|svg\+xml);base64,/.test(source);
  if (localImage && failed !== source) {
    return <img className="tm-task-icon" src={source} alt="" draggable={false} onError={() => setFailed(source)} />;
  }
  return processes ? <Layers aria-hidden="true" /> : <Activity aria-hidden="true" />;
}
