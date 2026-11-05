// How far each side of the pet window runs off the work area, in window pixels.
export interface Clip {
  top: number;
  left: number;
  right: number;
  bottom: number;
}

export interface CompanionBridge {
  interactive: (enabled: boolean) => void;
  move: (dx: number, dy: number) => void;
  hide: () => void;
  home: () => void;
  conversation: (storedId: string) => void;
  compact: () => void;
  expand: () => void;
  // The rectangle the character and its buttons fill; the shell keeps it on screen.
  body?: (rect: { x: number; y: number; width: number; height: number }) => void;
  // A chat or menu is open: bring the whole window onto the screen until it closes.
  reveal?: (open: boolean) => void;
  onClip?: (callback: (clip: Clip) => void) => () => void;
}

declare global {
  interface Window {
    companionDesktop?: CompanionBridge;
  }
}
