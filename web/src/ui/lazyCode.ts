import { type ComponentType, lazy } from "react";

// CodeMissing is code that did not download, such as a window's. React keeps
// the failure for as long as the page lives, and after an update the page's
// old files are gone from the device, so only reloading the page brings it back.
export class CodeMissing extends Error {}

// lazyCode loads a component's code and styles when it first renders, as
// React's lazy does, and marks a failure to download them as CodeMissing.
export function lazyCode<Props extends object>(load: () => Promise<ComponentType<Props>>) {
  return lazy(() =>
    load().then(
      (component) => ({ default: component }),
      (cause: unknown) => {
        throw new CodeMissing("the code did not load", { cause });
      },
    ),
  );
}
