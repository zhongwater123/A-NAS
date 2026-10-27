import { FileUp, FolderUp, Upload } from "lucide-react";
import { useRef } from "react";

import { PickedFile, pickedFromInput } from "../lib/pickedFiles";
import styles from "./files.module.css";

// FileUploadControl is the floating upload button and its menu: files, or a
// folder that keeps its structure.
export function FileUploadControl({
  menuOpen,
  onMenuOpen,
  onUpload,
}: {
  menuOpen: boolean;
  onMenuOpen: (open: boolean) => void;
  onUpload: (picked: PickedFile[]) => void;
}) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);
  return (
    <div className={styles.fileUploadControl}>
      {menuOpen && (
        <>
          <div className={styles.fileUploadScrim} onPointerDown={() => onMenuOpen(false)} />
          <div className={styles.fileUploadMenu} role="menu" aria-label="上传">
            <button
              role="menuitem"
              onClick={() => {
                onMenuOpen(false);
                fileInputRef.current?.click();
              }}
            >
              <FileUp />
              <span>
                <strong>上传文件</strong>
                <small>可多选</small>
              </span>
            </button>
            <button
              role="menuitem"
              onClick={() => {
                onMenuOpen(false);
                folderInputRef.current?.click();
              }}
            >
              <FolderUp />
              <span>
                <strong>上传文件夹</strong>
                <small>保留文件夹结构</small>
              </span>
            </button>
            <p>也可以把文件或文件夹直接拖进来</p>
          </div>
        </>
      )}
      <button
        className={styles.fileFloatingUpload}
        aria-label="上传到当前目录"
        title="上传到当前目录"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={() => onMenuOpen(!menuOpen)}
      >
        <Upload />
      </button>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        hidden
        aria-label="选择要上传的文件"
        onChange={(event) => {
          onUpload(pickedFromInput(event.target.files));
          event.target.value = "";
        }}
      />
      <input
        ref={(element) => {
          folderInputRef.current = element;
          element?.setAttribute("webkitdirectory", "");
        }}
        type="file"
        multiple
        hidden
        aria-label="选择要上传的文件夹"
        onChange={(event) => {
          onUpload(pickedFromInput(event.target.files));
          event.target.value = "";
        }}
      />
    </div>
  );
}
