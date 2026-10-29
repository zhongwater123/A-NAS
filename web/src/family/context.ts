import { createContext, type ReactNode, type RefObject, use } from "react";

import type { Toast } from "../ui/feedback";
import type { FamilyCatalog, FamilyMember, FamilyOverview } from "./api";

// FamilyContext is what every part of the window shares: who is reading, the
// family, the catalog, and the window's dialogs and toasts.
export interface Family {
  userId: string;
  overview: FamilyOverview;
  catalog: FamilyCatalog;
  reload: () => Promise<FamilyOverview | undefined>;
  toast: (toast: Toast) => void;
  prompt: (options: {
    title: string;
    label: string;
    value?: string;
    confirm?: string;
    maxLength?: number;
  }) => Promise<string | undefined>;
  confirm: (options: { title: string; message: ReactNode; confirm: string; danger?: boolean }) => Promise<boolean>;
  // container is the window, where dialogs open.
  container: RefObject<HTMLDivElement | null>;
  onShowPhoto: (assetId: string) => void;
}

export const FamilyContext = createContext<Family | undefined>(undefined);

export function useFamily(): Family {
  const family = use(FamilyContext);
  if (!family) throw new Error("useFamily outside the family window");
  return family;
}

// member finds a family member by account, for names, roles and avatars.
export function memberOf(family: Family, accountId: string): FamilyMember | undefined {
  return family.overview.members.find((member) => member.accountId === accountId);
}

// canDelete says whether the reader may delete something an account added:
// their own, or anyone's for a guardian.
export function canDelete(family: Family, authorId: string): boolean {
  return authorId === family.userId || family.overview.me.guardian;
}
