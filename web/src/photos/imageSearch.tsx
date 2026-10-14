import { Camera } from "lucide-react";
import type { ClipboardEvent, DragEvent } from "react";

import { APIError } from "../api";
import { createImageSearch, listImageSearchResults, type PhotoPage } from "./photosApi";

// An image to search the library by: what the browser made of the chosen,
// dropped or pasted file. The photo service gets this JPEG, never the file.
export interface ImageQuery { key: string; name: string; blob: Blob; url: string; width: number; height: number }

// What reading the image came to: undefined while local AI reads it.
export type ImageResult = { error?: string } | undefined;

// The photo service renders the detector's input at 1600 px; a larger image
// would only be scaled down there after a longer trip.
const queryEdge = 1600;
let queries = 0;

export const imageFile = (files?: FileList | null) => Array.from(files ?? []).find((file) => file.type.startsWith("image/"));

// pickImage lets the member choose one image to search by.
export function pickImage(onImage: (file: File) => void) {
  const input = document.createElement("input");
  input.type = "file";
  input.accept = "image/*";
  input.onchange = () => { const file = imageFile(input.files); if (file) onImage(file); };
  input.click();
}

// prepareImageQuery decodes any image the browser can show, turns it upright,
// puts it on white and scales it to fit 1600 px as a JPEG: formats the photo
// service does not read, such as WebP, AVIF or HEIC where the browser shows
// it, work too, and a big photo travels small.
export async function prepareImageQuery(file: Blob, name: string): Promise<ImageQuery> {
  const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  try {
    const scale = Math.min(1, queryEdge / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("canvas unavailable");
    context.fillStyle = "#fff";
    context.fillRect(0, 0, width, height);
    context.imageSmoothingQuality = "high";
    context.drawImage(bitmap, 0, 0, width, height);
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.92));
    if (!blob) throw new Error("JPEG encoding failed");
    return { key: `image-${++queries}`, name, blob, url: URL.createObjectURL(blob), width, height };
  } finally {
    bitmap.close();
  }
}

// The photo service's ID for each image, sent once; a send that failed is
// tried again next time.
const searches = new WeakMap<ImageQuery, Promise<string>>();
const encoded = new WeakSet<ImageQuery>();

function searchId(query: ImageQuery, fresh = false) {
  const kept = fresh ? undefined : searches.get(query);
  if (kept) return kept;
  const sent = createImageSearch(query.blob).then(({ id }) => { encoded.add(query); return id; });
  searches.set(query, sent);
  sent.catch(() => { if (searches.get(query) === sent) searches.delete(query); });
  return sent;
}

// Whether local AI has read the image already, so its results come at once.
export const isEncoded = (query: ImageQuery) => encoded.has(query);

const imageErrors: Record<string, string> = {
  unsupported_media_type: "无法读取这张图片，换一张试试",
  too_large: "图片太大，换一张小一些的试试",
};

// imageSearchPage loads a page of the photos like the image. When the photo
// service no longer keeps the search (it restarted, or the search sat idle),
// the image goes again and the page is asked for anew.
export async function imageSearchPage(query: ImageQuery, viewing: string, cursor: string): Promise<PhotoPage> {
  try {
    try {
      return await listImageSearchResults(await searchId(query), viewing, cursor);
    } catch (caught) {
      if (!(caught instanceof APIError && caught.code === "search_expired")) throw caught;
      return await listImageSearchResults(await searchId(query, true), viewing, cursor);
    }
  } catch (caught) {
    if (caught instanceof APIError && imageErrors[caught.code]) throw new Error(imageErrors[caught.code]);
    throw caught;
  }
}

// What a search box needs to take an image dropped onto or pasted into it.
export interface ImageDropProps {
  onDragOver: (event: DragEvent) => void;
  onDragLeave: (event: DragEvent) => void;
  onDrop: (event: DragEvent) => void;
  onPaste: (event: ClipboardEvent) => void;
}

// ImageSearchButton sits at the end of a search box.
export function ImageSearchButton({ onPick, className = "ph-search-image" }: { onPick: () => void; className?: string }) {
  return (
    <button type="button" className={className} aria-label="以图搜图" title="以图搜图：选择一张图片，或把图片拖到、粘贴到搜索框" onClick={onPick}>
      <Camera />
    </button>
  );
}

// ImageReading fills the results while local AI reads the image, which
// takes a few seconds on the NAS: the image itself, with a light sweeping
// down it.
export function ImageReading({ query, topInset }: { query: ImageQuery; topInset: number }) {
  const aspect = Math.min(1.8, Math.max(0.6, query.width / query.height));
  return (
    <div className="ph-image-reading" style={{ paddingTop: topInset }} role="status">
      <span className="ph-image-thumb" style={{ aspectRatio: `${aspect}` }}><img src={query.url} alt="" draggable={false} /></span>
      <strong>正在识别这张图片</strong>
      <p>本地 AI 在这台 NAS 上分析画面和画中物品，图片不会被保存</p>
    </div>
  );
}

// ImageSource shows the image photos are being found for and how far local
// AI has got with it; another image can take its place.
export function ImageSource({ query, result, over, drop, onReplace }: { query: ImageQuery; result: ImageResult; over: boolean; drop: ImageDropProps; onReplace: () => void }) {
  const aspect = Math.min(1.8, Math.max(0.6, query.width / query.height));
  const reading = result === undefined;
  return (
    <div className={`ph-similar-source ph-image-source${reading ? " reading" : ""}${over ? " image-drop" : ""}`} aria-busy={reading} {...drop}>
      <span className="ph-image-thumb" style={{ aspectRatio: `${aspect}` }}><img src={query.url} alt="" draggable={false} /></span>
      <div>
        <strong title={query.name}>{query.name}</strong>
        <small className={result?.error ? "failed" : undefined}>
          {reading ? "正在识别画面和画中物品…" : result.error ? "没能识别这张图片" : "按画面和画中物品的相似程度排列，最像的在前"}
        </small>
      </div>
      <button type="button" className="ph-image-replace" title="换一张图片搜索，也可以把图片拖到这里" onClick={onReplace}><Camera /><span>换一张</span></button>
    </div>
  );
}
