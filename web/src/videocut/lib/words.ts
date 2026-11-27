// forMember is a failure as VideoCut wrote it for the member, or nothing for its own diagnostics.
export function forMember(error?: string): string | undefined {
  return error && /[一-鿿]/.test(error) ? error : undefined;
}
