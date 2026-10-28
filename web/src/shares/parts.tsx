import {
  File,
  FileArchive,
  FileAudio,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileVideo,
  Folder,
  type LucideIcon,
  Presentation,
} from "lucide-react";

import { isPreviewableImage, isVideoFile } from "../lib/fileTypes";
import styles from "./shares.module.css";

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
    <span
      className={`${styles.shTile} ${styles[`sh-tile-${size}`] ?? ""} ${styles[`tone-${tone}`] ?? ""}`}
      aria-hidden="true"
    >
      <Icon />
    </span>
  );
}
