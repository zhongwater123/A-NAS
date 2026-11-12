// What a file is, judged by its name: a label for lists and whether the
// browser can show it in place.

export type FileCategory = "image" | "video" | "audio" | "pdf" | "sheet" | "document" | "archive" | "other";

// fileCategory sorts a file into the kinds a list tells apart by icon.
export function fileCategory(name: string): FileCategory {
  const extension = (name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "") ?? "";
  if (isPreviewableImage(name)) return "image";
  if (isVideoFile(name)) return "video";
  if (["mp3", "flac", "wav", "m4a", "aac"].includes(extension)) return "audio";
  if (extension === "pdf") return "pdf";
  if (["xls", "xlsx", "csv"].includes(extension)) return "sheet";
  if (["doc", "docx", "txt", "md"].includes(extension)) return "document";
  if (["zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz"].includes(extension)) return "archive";
  return "other";
}

const categoryLabels: Partial<Record<FileCategory, string>> = {
  image: "图片",
  video: "视频",
  audio: "音频",
  pdf: "PDF 文档",
  sheet: "电子表格",
  document: "文档",
};

export function fileTypeLabel(name: string): string {
  const label = categoryLabels[fileCategory(name)];
  if (label) return label;
  const extension = name.includes(".") ? name.split(".").pop() : "";
  return extension ? `${extension.toLocaleUpperCase("en-US")} 文件` : "文件";
}

export function isBrowserPreviewableImage(name: string): boolean {
  const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
  return ["jpg", "jpeg", "png", "gif", "webp", "bmp", "avif"].includes(extension ?? "");
}

export function isPDFPreviewable(name: string): boolean {
  return name.toLocaleLowerCase("en-US").endsWith(".pdf");
}

export function isTextPreviewable(name: string): boolean {
  const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
  return [
    "txt",
    "md",
    "markdown",
    "json",
    "yaml",
    "yml",
    "csv",
    "log",
    "xml",
    "html",
    "css",
    "js",
    "jsx",
    "ts",
    "tsx",
    "go",
    "py",
    "sh",
    "ini",
    "conf",
  ].includes(extension ?? "");
}

export function isOfficeDocument(name: string): boolean {
  const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
  return ["doc", "docx", "ppt", "pptx"].includes(extension ?? "");
}

export function isHEIFImage(name: string): boolean {
  return /\.(heic|heif)$/i.test(name);
}

export function isPreviewableImage(name: string): boolean {
  return isBrowserPreviewableImage(name) || isHEIFImage(name);
}

// isVideoFile follows the server's video types (internal/mediatype), which
// it shows a frame of. All but .ts: here that is TypeScript, shown as text.
export function isVideoFile(name: string): boolean {
  return /\.(mp4|mov|mkv|webm|avi|m4v|mpg|mpeg|mts|m2ts|flv|f4v|wmv|3gp|rmvb|rm|vob|ogv|asf|divx)$/i.test(name);
}
