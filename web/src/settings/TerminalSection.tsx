import { RotateCcw, SquareTerminal } from "lucide-react";
import { Suspense, lazy, useCallback, useEffect, useState } from "react";

import { readTerminalEnabled, type SessionStatus } from "../terminal";
import { Banner, IconTile, PageHeader, Pill } from "./parts";
import "./terminal.css";

const XtermSession = lazy(() => import("../XtermSession"));

type Availability = "checking" | "enabled" | "disabled" | "unreachable";

// TerminalSection is Settings → 终端. Settings keeps it mounted, hidden, while
// another section is shown, so the shell keeps running until the settings
// window closes.
export function TerminalSection({ username, hostname, active, onStatus }: {
  username: string;
  hostname?: string;
  active: boolean;
  onStatus: (status: SessionStatus) => void;
}) {
  const [availability, setAvailability] = useState<Availability>("checking");
  const [attempt, setAttempt] = useState(0);
  const [session, setSession] = useState(0);
  const [status, setStatus] = useState<SessionStatus>({ state: "connecting" });
  const [size, setSize] = useState<{ cols: number; rows: number }>();

  useEffect(() => {
    const controller = new AbortController();
    setAvailability("checking");
    readTerminalEnabled(controller.signal)
      .then((enabled) => setAvailability(enabled ? "enabled" : "disabled"))
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) setAvailability("unreachable");
      });
    return () => controller.abort();
  }, [attempt]);

  const report = useCallback((next: SessionStatus) => { setStatus(next); onStatus(next); }, [onStatus]);
  const restart = () => { report({ state: "connecting" }); setSession((value) => value + 1); };
  const resize = useCallback((cols: number, rows: number) => setSize({ cols, rows }), []);
  // Latin names get spaces around them in Chinese text.
  const device = hostname ? ` ${hostname} ` : "这台设备";

  if (availability !== "enabled") {
    return (
      <div className="set-page" hidden={!active}>
        <PageHeader title="终端" subtitle="在网页里打开这台设备的 Shell" />
        {availability === "checking" && <div className="set-card tm-notice tm-checking"><span className="loader" /><p>正在检查终端…</p></div>}
        {availability === "unreachable" && (
          <Banner tone="error">
            无法读取终端状态，请确认 A-NAS 产品服务正在运行。
            <button className="set-btn tm-banner-action" onClick={() => setAttempt((value) => value + 1)}>重试</button>
          </Banner>
        )}
        {availability === "disabled" && (
          <div className="set-card tm-notice">
            <IconTile tone="terminal" icon={SquareTerminal} size="lg" />
            <h3>终端尚未开启</h3>
            <p>终端让管理员以自己的 Linux 账号在{device}上运行命令。为安全起见它默认关闭，只能由设备的 root 开启：</p>
            <pre className="tm-code" aria-label="开启终端的命令"><code>{"echo ANAS_TERMINAL=enabled >> /etc/a-nas/anas-api.env\nsystemctl restart anas-api"}</code></pre>
            <p className="tm-notice-foot">开启后，系统升级会保留这个设置。</p>
            <button className="set-btn" onClick={() => setAttempt((value) => value + 1)}>重新检查</button>
          </div>
        )}
      </div>
    );
  }

  const ended = status.state === "ended";
  return (
    <div className="set-page tm-page" hidden={!active}>
      <PageHeader
        title="终端"
        subtitle={`以 ${username} 的 Linux 账号在${device}上运行的 Shell`}
        action={
          <div className="tm-actions">
            <StatusPill status={status} />
            <button className="set-btn" title="结束当前 Shell，打开一个新的" onClick={restart}><RotateCcw />新建会话</button>
          </div>
        }
      />
      <section className={`tm-console ${ended ? "ended" : ""}`} aria-label="Shell">
        <header className="tm-console-bar">
          <span className="tm-prompt"><span className={`tm-led ${status.state}`} aria-hidden="true" />{username}@{hostname || "a-nas"}</span>
          {size && <span className="tm-size">{size.cols} × {size.rows}</span>}
        </header>
        <div className="tm-screen">
          <Suspense fallback={<div className="tm-loading"><span className="loader" />正在载入终端…</div>}>
            <XtermSession key={session} active={active} onStatus={report} onSize={resize} />
          </Suspense>
        </div>
        {ended && (
          <div className="tm-ended" role="status">
            <span>{status.exitCode === undefined ? "终端连接已断开" : `Shell 已退出（代码 ${status.exitCode}）`}</span>
            <button className="set-btn primary" onClick={restart}><RotateCcw />新建会话</button>
          </div>
        )}
      </section>
      <p className="set-footnote">Shell 的文件权限与你在网页中一致，没有 root 或 sudo。切换到其他分区或最小化窗口时它继续运行，关闭设置窗口会结束它。</p>
    </div>
  );
}

function StatusPill({ status }: { status: SessionStatus }) {
  if (status.state === "connected") return <Pill tone="success">已连接</Pill>;
  if (status.state === "connecting") return <Pill>正在连接…</Pill>;
  return <Pill tone="warning">已结束</Pill>;
}
