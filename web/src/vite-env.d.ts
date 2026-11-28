/// <reference types="vite/client" />

interface ImportMetaEnv {
  // The version the page was built at, the Makefile's VERSION; unset in development.
  readonly VITE_ANAS_VERSION?: string;
  // "1" builds 剪辑中心's MSW stand-ins into the page, for the Fake preview only (videocut/mocks/browser.ts).
  readonly VITE_VIDEOCUT_STANDINS?: string;
}
