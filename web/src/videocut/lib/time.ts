// clock is a time in the film as minutes and seconds, as the timeline shows it.
export const clock = (seconds: number) =>
  `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2, "0")}`;

const secondsFormat = new Intl.NumberFormat("zh-CN", { style: "unit", unit: "second", maximumFractionDigits: 1 });

// duration is a length in seconds as words: 2秒, 1.5秒.
export const duration = (seconds: number) => secondsFormat.format(seconds);
