import { CircleAlert, RotateCcw, SquareTerminal } from "lucide-react";
import { Suspense, lazy, useCallback, useEffect, useState } from "react";

import { SessionStatus, readTerminalEnabled } from "./terminal";

const XtermSession = lazy(() => import("./XtermSession"));

type Availability = "checking" | "enabled" | "disabled" | "unreachable";

export function TerminalPanel() {
  const [availability, setAvailability] = useState<Availability>("checking");
  const [attempt, setAttempt] = useState(0);
  const [session, setSession] = useState(0);
  const [status, setStatus] = useState<SessionStatus>({ state: "connecting" });

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

  const reconnect = useCallback(() => {
    setStatus({ state: "connecting" });
    setSession((value) => value + 1);
  }, []);

  if (availability === "checking") {
    return <div className="center-state terminal-state"><span className="loader" /><h2>正在检查终端…</h2></div>;
  }
  if (availability === "unreachable") {
    return (
      <div className="center-state terminal-state">
        <CircleAlert /><h2>无法连接产品服务</h2><p>请确认 A-NAS 产品服务正在运行。</p>
        <button className="primary" onClick={() => setAttempt((value) => value + 1)}>重试</button>
      </div>
    );
  }
  if (availability === "disabled") {
    return (
      <div className="center-state terminal-state">
        <SquareTerminal /><h2>终端未启用</h2>
        <p>以 <code>ANAS_TERMINAL=enabled</code> 启动产品服务后，可在此打开设备 Shell。</p>
      </div>
    );
  }

  return (
    <div className="terminal-panel">
      <Suspense fallback={<div className="center-state terminal-state"><span className="loader" /><h2>正在载入终端…</h2></div>}>
        <XtermSession key={session} onStatus={setStatus} />
      </Suspense>
      {status.state === "ended" && (
        <div className="terminal-ended" role="status">
          <span>{status.exitCode === undefined ? "终端连接已断开" : `Shell 已退出（代码 ${status.exitCode}）`}</span>
          <button className="primary" onClick={reconnect}><RotateCcw size={14} />新建会话</button>
        </div>
      )}
    </div>
  );
}
