// SPDX-License-Identifier: GPL-3.0-or-later

import { type ReactNode, useEffect, useRef } from "react";

/**
 * A modal built on <dialog>: focus trapping and Escape come from the browser. Render it
 * only while it should be open; `onClose` runs when the user dismisses it.
 */
export function Dialog({
  title,
  onClose,
  children,
  wide = false,
}: {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const close = useRef(onClose);
  close.current = onClose;

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    // jsdom has no showModal.
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    const onCancel = (event: Event) => {
      event.preventDefault();
      close.current();
    };
    dialog.addEventListener("cancel", onCancel);
    return () => {
      dialog.removeEventListener("cancel", onCancel);
      if (typeof dialog.close === "function" && dialog.open) dialog.close();
    };
  }, []);

  return (
    <dialog ref={ref} className={wide ? "dialog dialog-wide" : "dialog"} aria-labelledby="dialog-title">
      <header className="dialog-header">
        <h2 id="dialog-title">{title}</h2>
        <button type="button" className="ghost" aria-label="Close" onClick={() => close.current()}>
          ×
        </button>
      </header>
      <div className="dialog-body">{children}</div>
    </dialog>
  );
}

/** A yes/no dialog for destructive actions; shows the action's error and stays open on failure. */
export function ConfirmDialog({
  title,
  children,
  confirmLabel,
  pending,
  error,
  onConfirm,
  onClose,
}: {
  title: ReactNode;
  children?: ReactNode;
  confirmLabel: string;
  pending: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog title={title} onClose={onClose}>
      {children}
      {error ? (
        <div className="error-box" role="alert">
          {error instanceof Error ? error.message : String(error)}
        </div>
      ) : null}
      <div className="actions">
        <button type="button" className="danger" onClick={onConfirm} disabled={pending}>
          {pending ? "Working…" : confirmLabel}
        </button>
        <button type="button" onClick={onClose}>
          Cancel
        </button>
      </div>
    </Dialog>
  );
}
