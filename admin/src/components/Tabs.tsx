// SPDX-License-Identifier: GPL-3.0-or-later

import type { ReactNode } from "react";

/** A row of tabs. Children are tab links or buttons that use `tabProps`. */
export function TabList({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {children}
    </div>
  );
}

/**
 * Props for one tab (a router `<Link>`). The selected state comes only from `selected`:
 * the router's own active marking is switched off, because it also matches links whose
 * path matches but whose search differs, which made several tabs look selected at once.
 */
export function tabProps(selected: boolean) {
  return {
    className: "tab",
    role: "tab",
    "aria-selected": selected,
    activeProps: {},
    inactiveProps: {},
  } as const;
}
