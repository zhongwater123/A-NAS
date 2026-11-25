import { Ellipsis } from "lucide-react";
import { DropdownMenu } from "radix-ui";

import styles from "./rowMenu.module.css";

export interface RowMenuItem {
  label: string;
  run: () => void;
}

// RowMenu is the … menu at the end of a list row, on Radix's dropdown menu.
// It opens outside the window, above every window, so no window clips it;
// the trigger takes the row's own icon-button style.
export function RowMenu({
  label,
  items,
  triggerClassName,
}: {
  /** The trigger's accessible name, such as “更多操作：交物业费”. */
  label: string;
  items: RowMenuItem[];
  triggerClassName?: string;
}) {
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger className={triggerClassName} aria-label={label}>
        <Ellipsis size={16} />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content className={styles.rowMenu} align="end" sideOffset={4}>
          {items.map((item) => (
            <DropdownMenu.Item key={item.label} className={styles.rowMenuItem} onSelect={item.run}>
              {item.label}
            </DropdownMenu.Item>
          ))}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
