import { AlertTriangle, Columns2, Download, Languages, Plus, RefreshCw, Rows3, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { messageOf, Spinner, useVoice, VoiceMark } from "./context";
import { clock, download, exportText, paragraphs, staleTranslation } from "./format";
import type { Player } from "./player";
import { Popover } from "./Popover";
import { activeSeq } from "./Transcript";
import { deleteTranslation, type Language, type RecordingDetail, requestTranslation, type Translation } from "./voiceApi";

interface Props {
  detail: RecordingDetail;
  player: Player;
  onChange: (detail: RecordingDetail) => void;
}

const quick = ["en", "ja", "ko"];

// TranslationView shows the transcript in another language beside the
// original, sentence by sentence.
export function TranslationView({ detail, player, onChange }: Props) {
  const voice = useVoice();
  const languages = voice.status?.languages ?? [];
  const names = useMemo(() => new Map(languages.map((language) => [language.code, language.name])), [languages]);
  const [chosen, setChosen] = useState(detail.translations.at(-1)?.language);
  const [bilingual, setBilingual] = useState(true);
  useEffect(() => {
    if (!chosen || !detail.translations.some((item) => item.language === chosen)) setChosen(detail.translations.at(-1)?.language);
  }, [detail.translations, chosen]);
  const translation = detail.translations.find((item) => item.language === chosen);
  const textModel = voice.status?.textModel.available ?? true;

  const translate = async (language: string) => {
    try {
      onChange(await requestTranslation(detail.id, language));
      setChosen(language);
    } catch (caught) {
      voice.toast(messageOf(caught, "无法开始翻译"), "error");
    }
  };
  const remove = async (item: Translation) => {
    try {
      await deleteTranslation(detail.id, item.language);
      onChange({ ...detail, translations: detail.translations.filter((other) => other.language !== item.language) });
    } catch (caught) {
      voice.toast(messageOf(caught, "删除失败"), "error");
    }
  };
  const missing = languages.filter((language) => !detail.translations.some((item) => item.language === language.code));
  const picker = (
    <Popover label="翻译成其他语言" className="vo-button subtle" align="start" trigger={<><Plus /><span>{detail.translations.length ? "翻译成…" : "选择语言"}</span></>}>
      {(close) => <LanguageGrid languages={missing} onPick={(code) => { close(); void translate(code); }} />}
    </Popover>
  );

  if (!detail.translations.length) {
    return (
      <div className="vo-empty">
        <Languages />
        <h3>把转写翻译成另一种语言</h3>
        <p>逐句翻译，与原文对照；名字和专有名词会保留。{!textModel && "（这台 NAS 还没有接入文本模型）"}</p>
        <div className="vo-quick">
          {quick.map((code) => <button key={code} type="button" className="vo-button" disabled={!textModel} onClick={() => void translate(code)}>{names.get(code) ?? code}</button>)}
          {picker}
        </div>
      </div>
    );
  }

  return (
    <div className="vo-translation">
      <div className="vo-toolbar">
        <div className="vo-pills" role="tablist" aria-label="译文语言">
          {detail.translations.map((item) => (
            <button key={item.language} type="button" role="tab" aria-selected={item.language === chosen} className={item.language === chosen ? "active" : ""} onClick={() => setChosen(item.language)}>
              {names.get(item.language) ?? item.language}
              {(item.status === "queued" || item.status === "running") && <Spinner small />}
              {item.status === "failed" && <AlertTriangle className="vo-pill-warn" />}
              {staleTranslation(detail, item) && <i className="vo-pill-dot" title="转写改过了" />}
            </button>
          ))}
          {picker}
        </div>
        {translation?.status === "ready" && (
          <>
            <div className="vo-segmented" role="radiogroup" aria-label="显示方式">
              <button type="button" role="radio" aria-checked={bilingual} className={bilingual ? "active" : ""} onClick={() => setBilingual(true)} title="原文与译文对照"><Columns2 />对照</button>
              <button type="button" role="radio" aria-checked={!bilingual} className={!bilingual ? "active" : ""} onClick={() => setBilingual(false)} title="只看译文"><Rows3 />仅译文</button>
            </div>
            <Popover label="导出译文" className="vo-icon-button" trigger={<Download />}>
              {(close) => (
                <div className="vo-menu" role="menu">
                  <button type="button" role="menuitem" onClick={() => { download(exportText(detail, "translation-txt", { translation })); close(); }}>纯文本 (.txt)</button>
                  <button type="button" role="menuitem" onClick={() => { download(exportText(detail, "translation-srt", { translation })); close(); }}>字幕 (.srt)</button>
                </div>
              )}
            </Popover>
          </>
        )}
        {translation && translation.status !== "queued" && translation.status !== "running" && (
          <button type="button" className="vo-icon-button" aria-label="删除这份译文" title="删除这份译文" onClick={() => void remove(translation)}><Trash2 /></button>
        )}
      </div>
      {translation && <TranslationBody detail={detail} translation={translation} bilingual={bilingual} player={player} language={names.get(translation.language) ?? translation.language} onRetry={() => void translate(translation.language)} />}
    </div>
  );
}

function LanguageGrid({ languages, onPick }: { languages: Language[]; onPick: (code: string) => void }) {
  if (!languages.length) return <p className="vo-menu-hint">已经译成所有语言</p>;
  return (
    <div className="vo-language-grid" role="menu">
      {languages.map((language) => <button key={language.code} type="button" role="menuitem" onClick={() => onPick(language.code)}>{language.name}</button>)}
    </div>
  );
}

function TranslationBody({ detail, translation, bilingual, player, language, onRetry }: {
  detail: RecordingDetail; translation: Translation; bilingual: boolean; player: Player; language: string; onRetry: () => void;
}) {
  const lines = useMemo(() => new Map(translation.lines.map((line) => [line.seq, line.text])), [translation.lines]);
  const speakers = useMemo(() => new Map(detail.speakerList.map((speaker) => [speaker.key, speaker])), [detail.speakerList]);
  const current = player.timeMs > 0 ? activeSeq(detail.utterances, player.timeMs) : undefined;
  if (translation.status === "queued" || translation.status === "running") {
    return (
      <div className="vo-job">
        <Spinner />
        <h3>正在译成{language}</h3>
        <p>{translation.status === "queued" ? "排在其他整理任务之后" : `已完成 ${Math.round(translation.progress * 100)}%`}</p>
        <div className="vo-progress"><i style={{ width: `${Math.max(4, translation.progress * 100)}%` }} /></div>
      </div>
    );
  }
  if (translation.status === "failed") {
    return (
      <div className="vo-job failed">
        <AlertTriangle />
        <h3>翻译没有完成</h3>
        <p>{translation.error?.code === "text_model_unavailable" ? "暂时连不上文本模型，稍后再试。" : "文本模型没有给出可用的译文。"}</p>
        <button type="button" className="vo-button primary" onClick={onRetry}><RefreshCw />重新翻译</button>
      </div>
    );
  }
  return (
    <div className="vo-scroll">
      {staleTranslation(detail, translation) && (
        <div className="vo-banner"><AlertTriangle /><span>转写在翻译之后修改过，译文可能与原文不一致。</span><button type="button" className="vo-button small" onClick={onRetry}><RefreshCw />重新翻译</button></div>
      )}
      {paragraphs(detail.utterances).map((turn) => {
        const speaker = speakers.get(turn.speaker);
        return (
          <article key={turn.utterances[0].seq} className={`vo-turn translated${bilingual ? "" : " only"}`}>
            {speaker && <VoiceMark speaker={speaker} size={28} />}
            <div className="vo-turn-body">
              <header><strong>{speaker?.label}</strong><button type="button" className="vo-stamp" onClick={() => player.play(turn.startMs)}>{clock(turn.startMs)}</button></header>
              {turn.utterances.map((utterance) => (
                <div key={utterance.seq} className={`vo-pair${current === utterance.seq ? " active" : ""}`} onClick={() => player.play(utterance.startMs)}>
                  {bilingual && <p className="vo-original">{utterance.text}</p>}
                  <p className="vo-translated">{lines.get(utterance.seq) ?? utterance.text}</p>
                </div>
              ))}
            </div>
          </article>
        );
      })}
      <p className="vo-generated">由 {translation.model} 翻译 · {new Date(translation.updatedAt).toLocaleString("zh-CN", { hour12: false })}</p>
    </div>
  );
}
