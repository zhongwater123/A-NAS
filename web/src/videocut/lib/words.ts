// forMember is a failure as VideoCut wrote it for the member, or nothing for its own diagnostics.
export function forMember(error?: string): string | undefined {
  return error && /[\u4e00-\u9fff]/.test(error) ? error : undefined;
}
