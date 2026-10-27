import {
  File,
  FileArchive,
  FileAudio,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileVideo,
  Folder,
  Presentation,
  type LucideIcon,
} from "lucide-react";
import { isPreviewableImage, isVideoFile } from "../lib/fileTypes";
import { displayName, person, type People } from "../people/people";
import { PersonAvatar } from "../people/PersonAvatar";

// A kind gives an item its label and tile colour in lists, the header and
// the send dialog.
interface Kind {
  label: string;
  tone: string;
  Icon: LucideIcon;
}
const extension = (name: string) => (name.includes(".") ? name.split(".").at(-1)!.toLowerCase() : "");
export const isAudio = (name: string) => /\.(mp3|flac|wav|m4a|aac|ogg|opus)$/i.test(name);
export function kindOf(name: string, isDir: boolean): Kind {
  if (isDir) return { label: "文件夹", tone: "folder", Icon: Folder };
  const ext = extension(name);
  if (isPreviewableImage(name) || ["heic", "heif"].includes(ext))
    return { label: "图片", tone: "image", Icon: FileImage };
  if (isVideoFile(name)) return { label: "视频", tone: "video", Icon: FileVideo };
  if (isAudio(name)) return { label: "音频", tone: "audio", Icon: FileAudio };
  if (ext === "pdf") return { label: "PDF", tone: "pdf", Icon: FileText };
  if (["xls", "xlsx", "csv", "numbers", "et"].includes(ext))
    return { label: "表格", tone: "sheet", Icon: FileSpreadsheet };
  if (["ppt", "pptx", "key", "dps"].includes(ext)) return { label: "演示文稿", tone: "slides", Icon: Presentation };
  if (["doc", "docx", "pages", "wps", "rtf", "odt"].includes(ext))
    return { label: "文档", tone: "doc", Icon: FileText };
  if (["txt", "md", "log", "json", "csv", "xml", "yaml", "yml"].includes(ext))
    return { label: "文本", tone: "doc", Icon: FileText };
  if (["zip", "7z", "rar", "tar", "gz", "tgz", "bz2", "xz"].includes(ext))
    return { label: "压缩包", tone: "archive", Icon: FileArchive };
  return { label: ext ? `${ext.toUpperCase()} 文件` : "文件", tone: "other", Icon: File };
}

export function EntryTile({ name, isDir, size = "md" }: { name: string; isDir: boolean; size?: "sm" | "md" | "lg" }) {
  const { tone, Icon } = kindOf(name, isDir);
  return (
    <span className={`sh-tile sh-tile-${size} tone-${tone}`} aria-hidden="true">
      <Icon />
    </span>
  );
}

// Avatar shows a person by id, completed from the shared people directory.
export function Avatar({
  people,
  id,
  username,
  size = "md",
}: {
  people: People;
  id: string;
  username?: string;
  size?: "xs" | "sm" | "md" | "lg";
}) {
  return (
    <PersonAvatar who={person(people, { id, username: username ?? id })} className={`sh-avatar sh-avatar-${size}`} />
  );
}
export function nameOf(people: People, id: string, username?: string): string {
  return displayName(person(people, { id, username: username ?? people.byId(id)?.username ?? id }));
}

// PeopleStack overlaps the first few faces of a share's recipients.
export function PeopleStack({ people, ids, max = 3 }: { people: People; ids: string[]; max?: number }) {
  const shown = ids.slice(0, ids.length > max ? max - 1 : max);
  return (
    <span className={`sh-stack count-${Math.min(ids.length, max)}`} aria-hidden="true">
      {shown.map((id) => (
        <Avatar key={id} people={people} id={id} size="sm" />
      ))}
      {ids.length > max && <span className="sh-avatar sh-avatar-sm sh-stack-more">+{ids.length - shown.length}</span>}
    </span>
  );
}

export function names(people: People, ids: string[], max = 3): string {
  const list = ids.slice(0, max).map((id) => nameOf(people, id));
  return ids.length > max ? `${list.join("、")} 等 ${ids.length} 人` : list.join("、");
}
