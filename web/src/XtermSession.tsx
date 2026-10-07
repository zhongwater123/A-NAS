import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef } from "react";

import { SessionStatus, exitCodeFromReason, resizeMessage, terminalSessionURL } from "./terminal";

const theme = {
  background: "#0d1117",
  foreground: "#d8dee9",
  cursor: "#5df2a0",
  selectionBackground: "rgba(93, 242, 160, .28)",
};

// One xterm instance bridged to one server-side shell; remounting starts a new shell.
export default function XtermSession({ onStatus }: { onStatus: (status: SessionStatus) => void }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const onStatusRef = useRef(onStatus);
  onStatusRef.current = onStatus;

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: '"JetBrains Mono", "Cascadia Mono", Menlo, Consolas, "Noto Sans Mono CJK SC", monospace',
      fontSize: 13,
      scrollback: 5000,
      theme,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);

    const socket = new WebSocket(terminalSessionURL());
    socket.binaryType = "arraybuffer";
    const encoder = new TextEncoder();
    const send = (data: string | Uint8Array<ArrayBuffer>) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(data);
    };
    // A minimized window has no size; fitting then would collapse the PTY.
    const refit = () => {
      if (host.clientWidth > 0 && host.clientHeight > 0) fit.fit();
    };

    const subscriptions = [
      term.onData((data) => send(encoder.encode(data))),
      term.onBinary((data) => send(Uint8Array.from(data, (char) => char.charCodeAt(0)))),
      term.onResize(({ cols, rows }) => send(resizeMessage(cols, rows))),
    ];
    socket.onopen = () => {
      refit();
      send(resizeMessage(term.cols, term.rows));
      term.focus();
      onStatusRef.current({ state: "connected" });
    };
    socket.onmessage = (event: MessageEvent) => {
      if (event.data instanceof ArrayBuffer) term.write(new Uint8Array(event.data));
    };
    socket.onclose = (event) => onStatusRef.current({ state: "ended", exitCode: exitCodeFromReason(event.reason) });

    const observer = new ResizeObserver(refit);
    observer.observe(host);
    refit();
    onStatusRef.current({ state: "connecting" });

    return () => {
      observer.disconnect();
      socket.onclose = null;
      socket.close();
      subscriptions.forEach((subscription) => subscription.dispose());
      term.dispose();
    };
  }, []);

  return <div className="xterm-host" ref={hostRef} />;
}
