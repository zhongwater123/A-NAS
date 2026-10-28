import { useEffect, useState } from "react";

import { getAIStatus, type PhotoAIStatus } from "./api";

const aiPollInterval = 15_000;

// useAIStatus is how far local AI has got with the photos, asked for again
// every 15 seconds; undefined while the photo service does not answer.
export function useAIStatus() {
  const [ai, setAI] = useState<PhotoAIStatus>();
  // Local AI progress; polling it does not count as using the library.
  useEffect(() => {
    let live = true;
    const poll = () =>
      getAIStatus().then(
        (value) => {
          if (live) setAI(value);
        },
        () => {
          if (live) setAI(undefined);
        },
      );
    void poll();
    const timer = window.setInterval(poll, aiPollInterval);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, []);
  return ai;
}
