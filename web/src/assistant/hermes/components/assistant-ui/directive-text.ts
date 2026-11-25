// A-NAS shim for Hermes Desktop's components/assistant-ui/directive-text: the
// one helper the transcript model uses, Desktop's own, without the chips,
// links and previews the rest of that component renders.
function needsQuoting(value: string): boolean {
  return /[\s()[\]{}<>"'`]/.test(value);
}

export function formatRefValue(value: string): string {
  if (!needsQuoting(value)) {
    return value;
  }
  if (!value.includes("`")) {
    return `\`${value}\``;
  }
  if (!value.includes('"')) {
    return `"${value}"`;
  }
  if (!value.includes("'")) {
    return `'${value}'`;
  }
  return value;
}
