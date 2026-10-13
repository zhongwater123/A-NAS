import { Hourglass, Scan, SearchX } from "lucide-react";

import { type PhotoAsset, type PhotoObjectsFound, previewURL } from "./photosApi";

// joinWords names a query's things the way a sentence would: 猫、狗和沙发.
export function joinWords(words: string[]) {
  if (words.length < 2) return words[0] ?? "";
  return `${words.slice(0, -1).join("、")}和${words.at(-1)}`;
}

// ObjectsStatus says what the object detector made of a search: the photos it
// found the query's things in, or that it found them in none, and how many
// photos it has not looked at yet, without which "none" would not be true.
export function ObjectsStatus({ objects, results }: { objects: PhotoObjectsFound; results: boolean }) {
  const things = joinWords(objects.words);
  return (
    <div className="ph-found" role="status">
      {objects.detected > 0
        ? <span className="ph-found-chip detected"><Scan />检测到{things}的照片 <strong>{objects.detected}</strong> 张</span>
        : <span className="ph-found-chip none"><SearchX />没有检测到{things}</span>}
      {objects.detected === 0 && results && <span className="ph-found-note">下面是画面最接近的照片</span>}
      {objects.undetected > 0 && (
        <span className="ph-found-note pending" title="本地 AI 会在设备空闲时继续识别，结果可能还会增加">
          <Hourglass />还有 {objects.undetected} 张尚未识别完
        </span>
      )}
    </div>
  );
}

// SimilarSource shows the photo similar photos were asked for.
export function SimilarSource({ asset }: { asset: PhotoAsset }) {
  const aspect = Math.min(1.8, Math.max(0.6, (asset.width || 1) / (asset.height || 1)));
  return (
    <div className="ph-similar-source">
      <img src={previewURL(asset)} alt="" style={{ aspectRatio: `${aspect}` }} draggable={false} />
      <div>
        <strong title={asset.name}>{asset.name}</strong>
        <small>按画面和画中物品的相似程度排列，最像的在前</small>
      </div>
    </div>
  );
}
