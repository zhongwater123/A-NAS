import { afterEach, beforeEach, vi } from "vitest";

// scrolls tells an element that scrolls (overflow-y: auto) from the rest.
export const scrolls = (element: Element) => element.isConnected && getComputedStyle(element).overflowY === "auto";

// sizedScrollers gives, in each test of the file or describe block that calls
// it, every element that scrolls a window-sized client area and nothing else a
// size, and stubs ResizeObserver to observe nothing. jsdom lays nothing out: a
// virtualized list or grid (virtual-core reads clientHeight and clientWidth)
// measures its scroller as 0 high and shows no rows. After each test it undoes
// every vi.stubGlobal, the calling file's own too.
export function sizedScrollers({ height = 800, width = 1000 } = {}) {
  const scrollerSize = { clientHeight: height, clientWidth: width };
  beforeEach(() => {
    for (const [name, size] of Object.entries(scrollerSize))
      Object.defineProperty(HTMLElement.prototype, name, {
        configurable: true,
        get() {
          return this instanceof HTMLElement && scrolls(this) ? size : 0;
        },
      });
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {
          /* no layout in jsdom */
        }
        unobserve() {
          /* none */
        }
        disconnect() {
          /* none */
        }
      },
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    // client* are Element.prototype's; without HTMLElement's stubs they show again.
    for (const name of Object.keys(scrollerSize)) Reflect.deleteProperty(HTMLElement.prototype, name);
  });
}
