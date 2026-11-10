// A member's assistant knows a sensitive number read from a photo only as a
// handle such as ⟦身份证号·1a2b3c⟧ (docs/architecture/photo-ocr.md) and writes
// the handle in its answer. The window turns each handle into a relative link
// it recognises, as it does for photos, and shows the number in its place,
// asked for with the member's own session: the number never passes the model.
export const textHandlePrefix = "#a-nas-text-handle/";

// A handle alone in a code span is taken with its backticks, so that it
// shows as the number and not as the link's source.
const label = "([^\\s·⟧[\\]()`]{1,12})";
const handle = new RegExp(`\`⟦${label}·([0-9a-f]{6})⟧\`|⟦${label}·([0-9a-f]{6})⟧`, "g");

export function linkTextHandles(text: string): string {
  return text.includes("⟦")
    ? text.replace(
        handle,
        (_, codeLabel?: string, codeId?: string, plainLabel?: string, plainId?: string) =>
          `[${codeLabel ?? plainLabel}](${textHandlePrefix}${codeId ?? plainId})`,
      )
    : text;
}

/** The handle a link names, when it is one. */
export function textHandleOf(href: string | undefined): string | undefined {
  if (!href?.startsWith(textHandlePrefix)) return undefined;
  const id = href.slice(textHandlePrefix.length);
  return /^[0-9a-f]{6}$/.test(id) ? id : undefined;
}
