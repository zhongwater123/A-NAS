import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { RefObject } from "react";

import { errorText } from "../api/errorMessages";
import { displayName } from "../people/people";
import { listUsers } from "../settings/api";
import { Modal } from "../ui/modal";
import { addMember, listMembers, removeMember, type WorkspaceMemberAccount } from "./api";
import type { Kit } from "./kit";
import styles from "./workspace.module.css";

const membersKey = ["workspace", "members"] as const;

// Members is where an administrator chooses who may use the workspace.
// Administrators always may; a member taken out loses it, and their agents'
// calls are refused, at once (ADR 0040).
export function Members({
  container,
  userId,
  kit,
  onClose,
}: {
  container: RefObject<HTMLElement | null>;
  userId: string;
  kit: Kit;
  onClose: () => void;
}) {
  const queries = useQueryClient();
  const members = useQuery({ queryKey: membersKey, queryFn: ({ signal }) => listMembers(signal) });
  // Roles come from the accounts list, which administrators read.
  const users = useQuery({ queryKey: ["workspace", "users"], queryFn: listUsers });
  const admins = new Set(users.data?.filter((user) => user.role === "admin").map((user) => user.id));
  const change = useMutation({
    mutationFn: ({ account, member }: { account: WorkspaceMemberAccount; member: boolean }) =>
      member ? addMember(account.id) : removeMember(account.id),
    onMutate: ({ account, member }) => {
      queries.setQueryData<WorkspaceMemberAccount[]>(membersKey, (current) =>
        current?.map((item) => (item.id === account.id ? { ...item, member } : item)),
      );
    },
    onError: (error) => kit.toast({ text: errorText(error, "workspace"), tone: "error" }),
    onSettled: () => queries.invalidateQueries({ queryKey: membersKey }),
  });
  return (
    <Modal title="成员" container={container} onClose={onClose}>
      {members.isError ? (
        <p className={styles.wsError} role="alert">
          {errorText(members.error, "workspace")}
        </p>
      ) : !members.data ? (
        <span className="loader" role="status" aria-label="正在读取成员" />
      ) : (
        <ul className={styles.wsMembers} aria-label="可以使用共享工作区的成员">
          {members.data.map((account) => {
            const admin = admins.has(account.id);
            return (
              <li key={account.id}>
                <label className={styles.wsMember}>
                  <input
                    type="checkbox"
                    checked={admin || account.member}
                    disabled={admin || account.id === userId}
                    onChange={(event) => change.mutate({ account, member: event.target.checked })}
                  />
                  <span>{displayName(account)}</span>
                  {admin && <span className={styles.wsFaint}>管理员</span>}
                </label>
              </li>
            );
          })}
        </ul>
      )}
    </Modal>
  );
}
