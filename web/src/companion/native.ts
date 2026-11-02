export interface CompanionBridge {
  interactive: (enabled: boolean) => void;
  move: (dx: number, dy: number) => void;
  hide: () => void;
  home: () => void;
  conversation: (storedId: string) => void;
  compact: () => void;
  expand: () => void;
}

declare global {
  interface Window {
    companionDesktop?: CompanionBridge;
  }
}
