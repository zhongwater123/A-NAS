import { ArrowUpRight, Copy, FileText, Music, Play } from "lucide-react";
import { type CSSProperties, type ReactNode, useState } from "react";

import { formatBytes } from "../lib/bytes";
import { copyText } from "../lib/clipboard";
import { calendarDate } from "../lib/time";
import type { WebSearchCategory, WebSearchInfobox, WebSearchResult } from "./api";
import styles from "./websearch.module.css";

const numbers = new Intl.NumberFormat("zh-CN");

// ResultList lays a page of results out as its tab shows them: tiles for
// pictures and videos, rows for tracks and torrents, and a column of pages,
// papers, packages and places for the others.
export function ResultList({
  category,
  results,
  onCopied,
}: {
  category: WebSearchCategory;
  results: WebSearchResult[];
  onCopied: (ok: boolean) => void;
}) {
  switch (category) {
    case "images":
      return (
        <ul className={styles.wsImages} aria-label="图片结果">
          {results.map((result, index) => (
            <ImageTile key={`${result.url}#${index}`} result={result} />
          ))}
        </ul>
      );
    case "videos":
      return (
        <ul className={styles.wsVideos} aria-label="视频结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <a href={result.url} target="_blank" rel="noopener noreferrer">
                <span className={styles.wsVideoCover}>
                  <Thumbnail src={result.thumbnail} fallback={<Play />} />
                  {result.durationSeconds !== undefined && (
                    <span className={styles.wsDuration}>{durationText(result.durationSeconds)}</span>
                  )}
                </span>
                <strong>{result.title}</strong>
              </a>
              <small>{joined(result.platform, result.author, published(result))}</small>
            </li>
          ))}
        </ul>
      );
    case "music":
      return (
        <ol className={styles.wsTracks} aria-label="音乐结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <a href={result.url} target="_blank" rel="noopener noreferrer">
                <span className={styles.wsCover}>
                  <Thumbnail src={result.thumbnail} fallback={<Music />} />
                </span>
                <span className={styles.wsTrackText}>
                  <strong>{result.title}</strong>
                  <small>{joined(result.author || result.content, result.platform || result.domain)}</small>
                </span>
                {result.durationSeconds !== undefined && (
                  <span className={styles.wsTrackTime}>{durationText(result.durationSeconds)}</span>
                )}
              </a>
            </li>
          ))}
        </ol>
      );
    case "torrents":
      return (
        <ul className={styles.wsTorrents} aria-label="种子结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <div className={styles.wsTorrentMain}>
                {result.magnet ? (
                  <a href={result.magnet} className={styles.wsTitle}>
                    {result.title}
                  </a>
                ) : (
                  <span className={styles.wsTitle}>{result.title}</span>
                )}
                <small>
                  {result.sizeBytes !== undefined && <span>{formatBytes(result.sizeBytes)}</span>}
                  {result.seeders !== undefined && (
                    <span className={result.seeders > 0 ? styles.wsSeeders : undefined}>做种 {result.seeders}</span>
                  )}
                  {result.leechers !== undefined && <span>下载 {result.leechers}</span>}
                  {published(result) && <span>{published(result)}</span>}
                  <ExternalLink href={result.url}>{result.engines.join("、") || result.domain}</ExternalLink>
                </small>
              </div>
              {result.magnet && (
                <button
                  type="button"
                  className={styles.wsCopy}
                  onClick={() => onCopied(copyText(result.magnet ?? ""))}
                  aria-label={`复制“${result.title}”的磁力链接`}
                >
                  <Copy />
                  <span>复制磁力链接</span>
                </button>
              )}
            </li>
          ))}
        </ul>
      );
    default:
      return (
        <ol className={category === "news" ? `${styles.wsList} ${styles.wsNews}` : styles.wsList} aria-label="搜索结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <div className={styles.wsListText}>
                <small className={styles.wsSource}>
                  <span>{result.domain}</span>
                  <span className={styles.wsEngines}>{result.engines.join("、")}</span>
                </small>
                <ExternalLink href={result.url} className={styles.wsTitle}>
                  {result.title}
                  {result.kind === "package" && result.version && (
                    <>
                      {" "}
                      <span className={styles.wsVersion}>{result.version}</span>
                    </>
                  )}
                </ExternalLink>
                <Details result={result} />
              </div>
              <ListThumb src={result.thumbnail} />
            </li>
          ))}
        </ol>
      );
  }
}

// Details are what a result says under its title: a paper's authors and
// journal, a package's maintainer and license, a place's address, then the
// summary and the links its kind has.
function Details({ result }: { result: WebSearchResult }) {
  const date = published(result);
  switch (result.kind) {
    case "paper":
      return (
        <>
          <p className={styles.wsMeta}>{joined(authorsText(result.authors), result.journal, date)}</p>
          {result.content && <p className={styles.wsSnippet}>{result.content}</p>}
          <Links>
            {result.pdf && (
              <ExternalLink href={result.pdf}>
                <FileText aria-hidden />
                PDF
              </ExternalLink>
            )}
            {result.doi && <ExternalLink href={`https://doi.org/${result.doi}`}>DOI {result.doi}</ExternalLink>}
          </Links>
        </>
      );
    case "package":
      return (
        <>
          {result.content && <p className={styles.wsSnippet}>{result.content}</p>}
          <p className={`${styles.wsMeta} ${styles.quiet}`}>
            {joined(
              result.author,
              result.license,
              result.popularity && `热度 ${popularityText(result.popularity)}`,
              date && `更新于 ${date}`,
            )}
          </p>
          <Links>
            {result.homepage && <ExternalLink href={result.homepage}>主页</ExternalLink>}
            {result.sourceCode && <ExternalLink href={result.sourceCode}>源代码</ExternalLink>}
          </Links>
        </>
      );
    case "place":
      return (
        <p className={styles.wsMeta}>
          {joined(
            result.address,
            result.latitude !== undefined &&
              result.longitude !== undefined &&
              `${result.latitude.toFixed(4)}, ${result.longitude.toFixed(4)}`,
          )}
        </p>
      );
    default:
      return result.content || date ? (
        <p className={styles.wsSnippet}>
          {date && <time dateTime={result.publishedAt}>{date}</time>}
          {result.content}
        </p>
      ) : null;
  }
}

function Links({ children }: { children: ReactNode[] }) {
  if (!children.some(Boolean)) return null;
  return <p className={styles.wsLinks}>{children}</p>;
}

// ImageTile keeps the picture's own shape: tiles in a row share a height and
// fill its width. A picture that does not load leaves no gap.
function ImageTile({ result }: { result: WebSearchResult }) {
  const [failed, setFailed] = useState(false);
  if (!result.thumbnail || failed) return null;
  const shape = result.width && result.height ? Math.min(Math.max(result.width / result.height, 0.5), 2.4) : 4 / 3;
  return (
    <li style={{ "--shape": shape } as CSSProperties}>
      <a href={result.url} target="_blank" rel="noopener noreferrer" title={result.title}>
        <img src={result.thumbnail} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />
        <span className={styles.wsImageCaption}>
          <strong>{result.title}</strong>
          <small>
            {joined(
              result.width && result.height ? `${result.width} × ${result.height}` : "",
              result.source || result.domain,
            )}
          </small>
        </span>
      </a>
    </li>
  );
}

// ListThumb is a result's picture beside its text; one that does not load
// gives the text the room.
function ListThumb({ src }: { src?: string }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) return null;
  return (
    <span className={styles.wsListThumb}>
      <img src={src} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />
    </span>
  );
}

// Thumbnail loads through A-NAS; one that fails shows the fallback mark on
// the tile's own color.
function Thumbnail({ src, fallback }: { src?: string; fallback?: ReactNode }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) {
    return (
      <span className={styles.wsThumbEmpty} aria-hidden>
        {fallback}
      </span>
    );
  }
  return <img src={src} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />;
}

export function Infobox({ infobox }: { infobox: WebSearchInfobox }) {
  const [failed, setFailed] = useState(false);
  return (
    <aside className={styles.wsInfobox} aria-label={infobox.title}>
      {infobox.thumbnail && !failed && (
        <img src={infobox.thumbnail} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />
      )}
      <div>
        <strong>{infobox.title}</strong>
        {infobox.content && <p>{infobox.content}</p>}
        {infobox.url && (
          <ExternalLink href={infobox.url} className={styles.wsInfoboxLink}>
            {hostOf(infobox.url)}
            <ArrowUpRight aria-hidden />
          </ExternalLink>
        )}
      </div>
    </aside>
  );
}

export function ExternalLink({ href, className, children }: { href: string; className?: string; children: ReactNode }) {
  return (
    <a href={href} className={className} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}

function joined(...parts: Array<string | false | undefined>): string {
  return parts.filter(Boolean).join(" · ");
}

// authorsText names up to three authors, as a reference list does.
export function authorsText(authors: string[] = []): string {
  return authors.length > 3 ? `${authors.slice(0, 3).join("、")} 等 ${authors.length} 人` : authors.join("、");
}

// popularityText groups a bare count's digits; a site's own wording, such as
// "50K+ pulls, 16 stars", stays as it is.
function popularityText(popularity: string): string {
  return /^\d+$/.test(popularity) ? numbers.format(Number(popularity)) : popularity;
}

function hostOf(address: string): string {
  try {
    return new URL(address).hostname.replace(/^www\./, "");
  } catch {
    return address;
  }
}

function published(result: WebSearchResult): string {
  return result.publishedAt ? calendarDate(result.publishedAt) : "";
}

// durationText reads 233 as 3:53 and 8189 as 2:16:29.
export function durationText(seconds: number): string {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = String(seconds % 60).padStart(2, "0");
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, "0")}:${rest}` : `${minutes}:${rest}`;
}
