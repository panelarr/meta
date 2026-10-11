// SPDX-License-Identifier: GPL-3.0-or-later

import { useEffect, useState } from "react";

export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");

  useEffect(() => {
    if (state === "idle") return;
    const timer = setTimeout(() => setState("idle"), 2000);
    return () => clearTimeout(timer);
  }, [state]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setState("copied");
    } catch {
      setState("failed");
    }
  };

  return (
    <button type="button" onClick={copy}>
      {state === "copied" ? "Copied" : state === "failed" ? "Copy failed: select and copy by hand" : label}
    </button>
  );
}
