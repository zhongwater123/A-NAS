/// <reference types="vite/client" />

interface ImportMetaEnv {
  // The version the page was built at, the Makefile's VERSION; unset in development.
  readonly VITE_ANAS_VERSION?: string;
}
