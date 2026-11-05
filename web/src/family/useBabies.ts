import { useEffect, useState } from "react";

import { type FamilyBaby, getFamily } from "./api";

// useBabies lists the family's babies for a member of the family, so another
// window can add photos to their channels. Anyone else, or a family center
// that does not answer, gets none.
export function useBabies(): FamilyBaby[] {
  const [babies, setBabies] = useState<FamilyBaby[]>([]);
  useEffect(() => {
    let live = true;
    getFamily().then(
      (overview) => {
        if (live && overview.me.status === "member") setBabies(overview.babies);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);
  return babies;
}
