import { FileText, History, ScanSearch, Search, ShieldCheck, Sunset, X } from "lucide-react";
import { FormEvent, useState } from "react";

import type { PhotoAIStatus } from "./photosApi";

interface Props {
  topInset: number;
  ai?: PhotoAIStatus;
  recent: string[];
  onSearch: (query: string) => void;
  onForget: () => void;
}

// Local AI compares the meaning of a whole photo with the sentence, so it
// does best with what a photo is about: the scene, the people and what they
// do, or a page or screen (docs/architecture/photo-ai.md#语义搜索).
const ways = [
  { icon: ScanSearch, title: "描述画面", text: "照片里有谁，在做什么", examples: ["草地上奔跑的孩子", "桌上的生日蛋糕", "抱着猫的人"] },
  { icon: Sunset, title: "场景与氛围", text: "地点、天气、时间和光线", examples: ["傍晚的海边", "下雪的街道", "夜晚的城市灯光"] },
  { icon: FileText, title: "截图与文档", text: "拍下的纸张和屏幕", examples: ["手机截图", "手写的笔记", "餐厅菜单"] },
];

// AISearchHome is where AI search starts: a sentence finds photos; below,
// the kinds of sentence that work, each example a search away.
export function AISearchHome({ topInset, ai, recent, onSearch, onForget }: Props) {
  const [query, setQuery] = useState("");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (query.trim()) onSearch(query.trim());
  };
  const status = !ai || ai.state === "unavailable" ? "" : ai.pending ? `已整理 ${ai.ready} / ${ai.ready + ai.pending + ai.failed} 张` : `已整理全部 ${ai.ready} 张`;

  return (
    <div className="ph-cards-scroll ph-discover" style={{ paddingTop: topInset }}>
      <section className="ph-hero" aria-label="AI 搜图">
        <h2>用一句话找照片</h2>
        <form className="ph-hero-search" role="search" onSubmit={submit}>
          <Search />
          <input autoFocus type="search" aria-label="用一句话搜索照片" placeholder="例如：傍晚海边散步的一家人" maxLength={200} value={query} onChange={(event) => setQuery(event.target.value)} />
          <button type="submit" className="ph-button primary" disabled={!query.trim()}>搜索</button>
        </form>
        {recent.length > 0 && (
          <div className="ph-hero-recent" aria-label="最近搜索">
            <History />
            {recent.map((item) => <button type="button" key={item} onClick={() => onSearch(item)}>{item}</button>)}
            <button type="button" className="ph-hero-forget" aria-label="清除最近搜索" title="清除最近搜索" onClick={onForget}><X /></button>
          </div>
        )}
        <p className="ph-hero-note"><ShieldCheck />照片在这台 NAS 上由本地 AI 识别，不会离开设备{status ? ` · ${status}` : ""}</p>
      </section>

      <section className="ph-ways" aria-label="可以这样搜">
        <header><h3>可以这样搜</h3><small>描述整张照片的内容，比只说一件小东西更容易找到</small></header>
        <div className="ph-ways-grid">
          {ways.map(({ icon: Icon, title, text, examples }) => (
            <article className="ph-way" key={title}>
              <span className="ph-way-icon"><Icon /></span>
              <h4>{title}</h4>
              <p>{text}</p>
              <div className="ph-way-examples">
                {examples.map((example) => <button type="button" key={example} aria-label={`搜索 ${example}`} onClick={() => onSearch(example)}>{example}</button>)}
              </div>
            </article>
          ))}
        </div>
      </section>
    </div>
  );
}
