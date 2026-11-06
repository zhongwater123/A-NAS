// Defer even the property access: private/sandboxed browsers may throw when
// accessing localStorage, before getItem or setItem can be called.
export const deviceStorage: Pick<Storage, "getItem" | "setItem"> = {
  getItem: (key) => window.localStorage.getItem(key),
  setItem: (key, value) => window.localStorage.setItem(key, value),
};
