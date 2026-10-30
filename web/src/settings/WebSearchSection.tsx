import { Download, FileText, Globe, Magnet, Scale } from "lucide-react";
import { Switch } from "radix-ui";
import { useEffect, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import {
  categoryLabels,
  licenseTextURL,
  readWebSearchLicense,
  sourceURL,
  updateWebSearch,
  type WebSearchCategory,
  type WebSearchLicense,
  type WebSearchStatus,
} from "../websearch/api";
import { publishWebSearch, useWebSearch } from "../websearch/status";
import { Banner, Group, IconTile, PageHeader, Pill, type Tone } from "./parts";
import styles from "./settings.module.css";

const runningText: Record<WebSearchStatus["running"], { text: string; tone: Tone }> = {
  running: { text: "运行中", tone: "success" },
  starting: { text: "启动中", tone: "accent" },
  stopped: { text: "已停止", tone: "neutral" },
  failed: { text: "出错", tone: "danger" },
};

const engineCategories: WebSearchCategory[] = ["web", "images", "videos", "news", "torrents"];

// WebSearchSection is Settings → 网页搜索, shown to administrators only
// (docs/specs/web-search.md): the switches of web search and its torrent tab,
// the engines it asks, and SearXNG's license and source (ADR 0030).
export function WebSearchSection() {
  const status = useWebSearch();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const change = async (next: { enabled: boolean; torrents: boolean }) => {
    setBusy(true);
    setError("");
    try {
      publishWebSearch(await updateWebSearch(next));
    } catch (caught) {
      setError(errorText(caught, "webSearch"));
    } finally {
      setBusy(false);
    }
  };
  const state = status && (status.enabled ? runningText[status.running] : { text: "已关闭", tone: "neutral" as Tone });
  return (
    <>
      <PageHeader title="网页搜索" subtitle="搜索请求从这台 NAS 发往下列搜索引擎" />
      {error && (
        <Banner tone="error" onClose={() => setError("")}>
          {error}
        </Banner>
      )}
      <Group title="开关">
        <div className={styles.setRow}>
          <IconTile tone="accent" icon={Globe} />
          <div className={styles.setRowText}>
            <span>
              网页搜索
              {state && <Pill tone={state.tone}>{state.text}</Pill>}
            </span>
            <small>关闭后所有账号都看不到网页搜索，NAS 不再访问任何搜索引擎。</small>
          </div>
          <Switch.Root
            className={styles.setSwitch}
            aria-label="网页搜索"
            checked={status?.enabled ?? false}
            disabled={!status || busy}
            onCheckedChange={(enabled) => status && void change({ enabled, torrents: status.torrents })}
          >
            <Switch.Thumb />
          </Switch.Root>
        </div>
        <div className={styles.setRow}>
          <IconTile tone="warning" icon={Magnet} />
          <div className={styles.setRowText}>
            <span>种子搜索</span>
            <small>种子站的结果多为未经授权的影视内容。</small>
          </div>
          <Switch.Root
            className={styles.setSwitch}
            aria-label="种子搜索"
            checked={status?.torrents ?? false}
            disabled={!status?.enabled || busy}
            onCheckedChange={(torrents) => status && void change({ enabled: status.enabled, torrents })}
          >
            <Switch.Thumb />
          </Switch.Root>
        </div>
      </Group>
      {status && (
        <Group title="搜索引擎">
          {engineCategories.map((category) => (
            <div key={category} className={styles.setRow}>
              <div className={styles.setRowText}>
                <span>{categoryLabels[category]}</span>
                <small>{status.engines[category].join("、")}</small>
              </div>
            </div>
          ))}
        </Group>
      )}
      <LicenseGroup />
    </>
  );
}

function LicenseGroup() {
  const [license, setLicense] = useState<WebSearchLicense | null>();
  const [failed, setFailed] = useState("");
  useEffect(() => {
    let active = true;
    readWebSearchLicense()
      .then((next) => active && setLicense(next))
      .catch((error: unknown) => {
        if (!active) return;
        if (error instanceof APIError && error.status === 404) setLicense(null);
        else setFailed(errorText(error));
      });
    return () => {
      active = false;
    };
  }, []);
  return (
    <Group title="开源许可">
      {license === undefined && !failed && <div className={styles.setRow}>正在读取…</div>}
      {failed && <div className={styles.setRow}>{failed}</div>}
      {license === null && <div className={styles.setRow}>这个版本没有附带网页搜索的运行环境。</div>}
      {license && (
        <>
          <div className={styles.setRow}>
            <IconTile tone="logs" icon={Scale} />
            <div className={styles.setRowText}>
              <span>
                {license.name}
                <Pill outline>{license.license}</Pill>
              </span>
              <small>版本 {license.version}</small>
            </div>
            <a className={styles.setBtn} href={licenseTextURL} target="_blank" rel="noopener noreferrer">
              <FileText />
              许可证
            </a>
            {license.source && (
              <a className={styles.setBtn} href={sourceURL} download>
                <Download />
                下载源码
              </a>
            )}
          </div>
          {license.packages.length > 0 && (
            <details className={styles.setPackages}>
              <summary>运行环境中的 {license.packages.length} 个软件包</summary>
              <table>
                <thead>
                  <tr>
                    <th>软件包</th>
                    <th>版本</th>
                    <th>许可证</th>
                  </tr>
                </thead>
                <tbody>
                  {license.packages.map((item) => (
                    <tr key={item.name}>
                      <td>{item.name}</td>
                      <td>{item.version}</td>
                      <td>{item.license || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </details>
          )}
        </>
      )}
    </Group>
  );
}
