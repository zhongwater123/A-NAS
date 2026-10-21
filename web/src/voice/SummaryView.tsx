import {
  AlertTriangle, BookOpenText, Briefcase, CheckCircle2, Copy, Download, GraduationCap, MessagesSquare, Mic, NotebookPen, PhoneCall, Plus,
  RefreshCw, Sparkles, Trash2,
} from "lucide-react";
import { type ReactNode, useEffect, useMemo, useState } from "react";

import { messageOf, Spinner, useVoice, Waveform } from "./context";
import { download, exportText, staleSummary } from "./format";
import type { Player } from "./player";
import { Popover } from "./Popover";
import { deleteSummary, type RecordingDetail, requestSummary, type Summary, type Template } from "./voiceApi";

interface Props {
  detail: RecordingDetail;
  player: Player;
  onChange: (detail: RecordingDetail) => void;
}

export const templateIcons: Record<string, ReactNode> = {
  general: <Sparkles />, meeting: <Briefcase />, lecture: <GraduationCap />, interview: <MessagesSquare />,
  call: <PhoneCall />, talk: <Mic />, memo: <NotebookPen />,
};

// TemplateGrid offers the scene templates as cards.
export function TemplateGrid({ templates, selected, onPick, compact }: { templates: Template[]; selected?: string; onPick: (id: string) => void; compact?: boolean }) {
  return (
    <div className={`vo-templates${compact ? " compact" : ""}`} role="radiogroup" aria-label="场景模板">
      {templates.map((template) => (
        <button key={template.id} type="button" role="radio" aria-checked={selected === template.id} className={selected === template.id ? "active" : ""} onClick={() => onPick(template.id)}>
          <span className="vo-template-icon">{templateIcons[template.id] ?? <BookOpenText />}</span>
          <span><strong>{template.name}</strong><small>{template.description}</small></span>
        </button>
      ))}
    </div>
  );
}

// SummaryView shows the recording summarised for a scene: an overview, the
// template's sections and the tasks agreed on.
export function SummaryView({ detail, player, onChange }: Props) {
  const voice = useVoice();
  const templates = voice.status?.templates ?? [];
  const byID = useMemo(() => new Map(templates.map((template) => [template.id, template])), [templates]);
  const [chosen, setChosen] = useState(detail.summaries.at(-1)?.template);
  useEffect(() => {
    if (!chosen || !detail.summaries.some((item) => item.template === chosen)) setChosen(detail.summaries.at(-1)?.template);
  }, [detail.summaries, chosen]);
  const summary = detail.summaries.find((item) => item.template === chosen);
  const textModel = voice.status?.textModel.available ?? true;

  const summarize = async (template: string) => {
    try {
      onChange(await requestSummary(detail.id, template));
      setChosen(template);
    } catch (caught) {
      voice.toast(messageOf(caught, "无法开始总结"), "error");
    }
  };
  const remove = async (item: Summary) => {
    try {
      await deleteSummary(detail.id, item.template);
      onChange({ ...detail, summaries: detail.summaries.filter((other) => other.template !== item.template) });
    } catch (caught) {
      voice.toast(messageOf(caught, "删除失败"), "error");
    }
  };
  const unused = templates.filter((template) => !detail.summaries.some((item) => item.template === template.id));

  if (!detail.summaries.length) {
    return (
      <div className="vo-scroll">
        <div className="vo-summary-start">
          <h3>选一个场景，让 AI 整理这段录音</h3>
          <p>按场景的栏目提炼要点、结论和待办，引用里写清是谁说的。{!textModel && "（这台 NAS 还没有接入文本模型）"}</p>
          <TemplateGrid templates={templates} onPick={(id) => { if (textModel) void summarize(id); }} />
        </div>
      </div>
    );
  }

  return (
    <div className="vo-summary">
      <div className="vo-toolbar">
        <div className="vo-pills" role="tablist" aria-label="总结">
          {detail.summaries.map((item) => (
            <button key={item.template} type="button" role="tab" aria-selected={item.template === chosen} className={item.template === chosen ? "active" : ""} onClick={() => setChosen(item.template)}>
              {templateIcons[item.template]}{byID.get(item.template)?.name ?? item.template}
              {(item.status === "queued" || item.status === "running") && <Spinner small />}
              {item.status === "failed" && <AlertTriangle className="vo-pill-warn" />}
              {staleSummary(detail, item) && <i className="vo-pill-dot" title="转写或发言人改过了" />}
            </button>
          ))}
          {unused.length > 0 && (
            <Popover label="换一个场景总结" className="vo-button subtle" align="start" trigger={<><Plus /><span>其他场景</span></>}>
              {(close) => <div className="vo-template-menu"><TemplateGrid compact templates={unused} onPick={(id) => { close(); void summarize(id); }} /></div>}
            </Popover>
          )}
        </div>
        {summary?.status === "ready" && summary.content && (
          <>
            <button type="button" className="vo-icon-button" aria-label="复制总结" title="复制总结"
              onClick={() => { void navigator.clipboard?.writeText(exportText(detail, "summary-md", { summary, template: byID.get(summary.template) }).text); voice.toast("已复制为 Markdown"); }}>
              <Copy />
            </button>
            <button type="button" className="vo-icon-button" aria-label="导出总结" title="导出为 Markdown"
              onClick={() => download(exportText(detail, "summary-md", { summary, template: byID.get(summary.template) }))}>
              <Download />
            </button>
          </>
        )}
        {summary && summary.status !== "queued" && summary.status !== "running" && (
          <button type="button" className="vo-icon-button" aria-label="删除这份总结" title="删除这份总结" onClick={() => void remove(summary)}><Trash2 /></button>
        )}
      </div>
      {summary && <SummaryBody detail={detail} summary={summary} template={byID.get(summary.template)} player={player} onRedo={() => void summarize(summary.template)} />}
    </div>
  );
}

// stamp makes the m:ss at the start of an item play from there.
function Stamped({ text, player }: { text: string; player: Player }) {
  const match = /^(\d{1,2}):(\d{2})(?::(\d{2}))?\s*/.exec(text);
  if (!match) return <>{text}</>;
  const ms = match[3] ? ((+match[1]) * 3600 + (+match[2]) * 60 + (+match[3])) * 1000 : ((+match[1]) * 60 + (+match[2])) * 1000;
  return <><button type="button" className="vo-stamp" onClick={() => player.play(ms)}>{match[0].trim()}</button>{text.slice(match[0].length)}</>;
}

function SummaryBody({ detail, summary, template, player, onRedo }: { detail: RecordingDetail; summary: Summary; template?: Template; player: Player; onRedo: () => void }) {
  if (summary.status === "queued" || summary.status === "running") {
    return (
      <div className="vo-job">
        <Waveform bars={11} />
        <h3>正在写{template?.name ?? "总结"}</h3>
        <p>{summary.status === "queued" ? "排在其他整理任务之后" : detail.durationMs > 3_600_000 ? "录音较长，会分段整理再合并，需要几分钟" : "通常不到一分钟"}</p>
      </div>
    );
  }
  if (summary.status === "failed" || !summary.content) {
    return (
      <div className="vo-job failed">
        <AlertTriangle />
        <h3>总结没有完成</h3>
        <p>{summary.error?.code === "text_model_unavailable" ? "暂时连不上文本模型，稍后再试。" : "文本模型没有给出可用的总结。"}</p>
        <button type="button" className="vo-button primary" onClick={onRedo}><RefreshCw />重新总结</button>
      </div>
    );
  }
  const content = summary.content;
  return (
    <div className="vo-scroll">
      <article className="vo-summary-sheet">
        {staleSummary(detail, summary) && (
          <div className="vo-banner"><AlertTriangle /><span>转写或发言人在总结之后改过了。</span><button type="button" className="vo-button small" onClick={onRedo}><RefreshCw />重新总结</button></div>
        )}
        <header>
          <span className="vo-summary-kind">{template && templateIcons[template.id]}{template?.name}</span>
          <h3>{content.title || detail.title}</h3>
          {content.keywords.length > 0 && <div className="vo-keywords">{content.keywords.map((keyword) => <span key={keyword}>{keyword}</span>)}</div>}
        </header>
        {content.overview && <section className="vo-overview"><h4>概述</h4><p>{content.overview}</p></section>}
        {content.sections.filter((section) => section.items.length).map((section) => (
          <section key={section.key} className="vo-section">
            <h4>{section.title}</h4>
            <ul>{section.items.map((item, index) => <li key={index}><Stamped text={item} player={player} /></li>)}</ul>
          </section>
        ))}
        {content.actions.length > 0 && (
          <section className="vo-section actions">
            <h4>待办事项</h4>
            <ul>
              {content.actions.map((action, index) => (
                <li key={index}>
                  <CheckCircle2 />
                  <span>{action.task}</span>
                  {action.owner && <em className="owner">{action.owner}</em>}
                  {action.due && <em className="due">{action.due}</em>}
                </li>
              ))}
            </ul>
          </section>
        )}
        <footer className="vo-generated">
          由 {summary.model} 根据转写整理，可能有遗漏或误解，重要内容请回听原声 · {new Date(summary.updatedAt).toLocaleString("zh-CN", { hour12: false })}
          <button type="button" className="vo-link" onClick={onRedo}>重新总结</button>
        </footer>
      </article>
    </div>
  );
}
