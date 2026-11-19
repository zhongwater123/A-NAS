import { afterEach, beforeEach, vi } from "vitest";

// scrolls tells an element that scrolls (overflow-y: auto) from the rest.
export const scrolls = (element: Element) => element.isConnected && getComputedStyle(element).overflowY === "auto";

// sizedScrollers gives, in each test of the file or describe block that calls
// it, every element that scrolls a window-sized view and nothing else a size,
// and stubs ResizeObserver to observe nothing. jsdom lays nothing out: a
// virtualized list or grid measures its scroller as 0 high and, once it has
// measured, shows no rows. virtual-core measures it by offset* before 3.19
// and by client* since.
export function sizedScrollers({ height = 800, width = 1000 } = {}) {
  const sizes = Object.getOwnPropertyDescriptors(HTMLElement.prototype);
  const scrollerSize = { offsetHeight: height, offsetWidth: width, clientHeight: height, clientWidth: width };
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
    for (const name of ["offsetHeight", "offsetWidth"] as const)
      Object.defineProperty(HTMLElement.prototype, name, sizes[name]);
    // client* are Element.prototype's; without HTMLElement's stubs they show again.
    for (const name of ["clientHeight", "clientWidth"] as const) Reflect.deleteProperty(HTMLElement.prototype, name);
  });
}
