import React, {
  createContext,
  useContext,
  useCallback,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { AppState } from "react-native";
import { createNasClient } from "./nas/native";
import { errorText, type Session } from "./nas/client";
import { ChatController } from "./chat/controller";
import { TransferEngine } from "./transfers/engine";

type Runtime = {
  nas: ReturnType<typeof createNasClient>;
  session: Session | null;
  chat?: ChatController;
  transfers?: TransferEngine;
  booting: boolean;
  error: string;
  login(address: string, user: string, password: string): Promise<void>;
  logout(): Promise<void>;
  refreshSession(): Promise<void>;
};
const Context = createContext<Runtime | null>(null);
export function RuntimeProvider({ children }: { children: ReactNode }) {
  const [nas] = useState(createNasClient);
  const [session, setSession] = useState<Session | null>(null);
  const [booting, setBooting] = useState(true),
    [error, setError] = useState("");
  const [services, setServices] = useState<{
    chat?: ChatController;
    transfers?: TransferEngine;
  }>({});
  const activate = useCallback(
    (next: Session | null) => {
      setSession(next);
      if (!next || next.user.mustChangePassword) {
        setServices({});
        return;
      }
      // React Native's socket accepts native request headers; the DOM type omits this overload.
      const NativeSocket = WebSocket as unknown as new (
        url: string,
        protocols: string[] | undefined,
        options: { headers: Record<string, string> },
      ) => WebSocket;
      const chat = new ChatController(
        nas,
        (url) => new NativeSocket(url, undefined, { headers: nas.headers }),
      );
      const transfers = new TransferEngine(nas);
      setServices({ chat, transfers });
    },
    [nas],
  );
  useEffect(() => {
    nas.setExpiredHandler(() => {
      setError("登录已过期，请重新登录。");
      activate(null);
    });
    void nas
      .restore()
      .then(activate)
      .catch((e) => setError(errorText(e)))
      .finally(() => setBooting(false));
    return () => nas.setExpiredHandler(() => {});
  }, [nas, activate]);
  useEffect(() => {
    const { chat, transfers } = services;
    if (!chat || !transfers) return;
    void chat.start();
    void transfers.restore().catch((e) => setError(errorText(e)));
    const subscription = AppState.addEventListener("change", (state) => {
      chat.setActive(state === "active");
      if (state === "active") transfers.foreground();
      else void transfers.suspend().catch((e) => setError(errorText(e)));
    });
    return () => {
      subscription.remove();
      chat.dispose();
      void transfers.dispose();
    };
  }, [services]);
  async function login(address: string, user: string, password: string) {
    setError("");
    activate(await nas.login(address, user, password));
  }
  async function logout() {
    services.chat?.dispose();
    await services.transfers?.dispose();
    try {
      await nas.logout();
    } finally {
      setServices({});
      setSession(null);
    }
  }
  async function refreshSession() {
    activate(await nas.restore());
  }
  return (
    <Context.Provider
      value={{
        nas,
        session,
        ...services,
        booting,
        error,
        login,
        logout,
        refreshSession,
      }}
    >
      {children}
    </Context.Provider>
  );
}
export function useRuntime() {
  const value = useContext(Context);
  if (!value) throw new Error("Missing runtime");
  return value;
}
