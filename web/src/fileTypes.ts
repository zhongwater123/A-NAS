// What a file is, judged by its name: a label for lists and whether the
// browser can show it in place.

export function fileTypeLabel(name: string): string {
  const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
  if (["jpg", "jpeg", "png", "gif", "webp", "heic"].includes(extension ?? "")) return "图片";
  if (["mp4", "mkv", "mov", "avi", "webm"].includes(extension ?? "")) return "视频";
  if (["mp3", "flac", "wav", "m4a", "aac"].includes(extension ?? "")) return "音频";
  if (extension === "pdf") return "PDF 文档";
  if (["xls", "xlsx", "csv"].includes(extension ?? "")) return "电子表格";
  if (["doc", "docx", "txt", "md"].includes(extension ?? "")) return "文档";
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
  return ["txt", "md", "markdown", "json", "yaml", "yml", "csv", "log", "xml", "html", "css", "js", "jsx", "ts", "tsx", "go", "py", "sh", "ini", "conf"].includes(extension ?? "");
}

export function isOfficeDocument(name: string): boolean {
  const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
  return ["doc", "docx", "ppt", "pptx"].includes(extension ?? "");
}
