// RateMeter turns a growing byte count into a speed over the last few
// seconds. It does not care where the bytes came from, so many small files
// that each finish in a fraction of a second still add up to a speed.
export class RateMeter {
  private samples: Array<{ at: number; bytes: number }> = [];

  constructor(private readonly window = 3000) {}

  // add records the count reached at a moment; counts never go down.
  add(at: number, bytes: number) {
    const last = this.samples.at(-1);
    if (last && at <= last.at) {
      last.bytes = Math.max(last.bytes, bytes);
      return;
    }
    // After a pause, measure from the last second rather than from before
    // the pause, so the speed shows at once instead of creeping up.
    if (last && at - last.at > this.window) this.samples = [{ at: at - 1000, bytes: last.bytes }];
    this.samples.push({ at, bytes });
  }

  // rate is bytes per second since the oldest sample still inside the
  // window. Measuring up to now, rather than up to the last sample, lets the
  // speed fall to zero within the window once nothing moves.
  rate(now: number): number {
    const cutoff = now - this.window;
    // Keep one sample at or before the cutoff as the baseline.
    while (this.samples.length > 1 && this.samples[1].at <= cutoff) this.samples.shift();
    if (this.samples.length < 2) return 0;
    const base = this.samples[0];
    const moved = this.samples[this.samples.length - 1].bytes - base.bytes;
    // At least a second, so the first instants do not spike.
    const elapsed = Math.max(now - base.at, 1000);
    return Math.max(0, (moved / elapsed) * 1000);
  }
}
