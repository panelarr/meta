// SPDX-License-Identifier: GPL-3.0-or-later

import type { ReactNode } from "react";
import { errorMessage } from "../api/errors";

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <p className="muted" role="status">
      <span className="spinner" aria-hidden="true" /> {label}
    </p>
  );
}

/** Shows an API error's message. Renders nothing without an error. */
export function ErrorBox({ error, onRetry, title }: { error: unknown; onRetry?: () => void; title?: string }) {
  if (!error) return null;
  return (
    <div className="error-box" role="alert">
      <div>
        {title ? <strong>{title}: </strong> : null}
        {errorMessage(error)}
      </div>
      {onRetry ? (
        <button type="button" className="small" onClick={onRetry}>
          Retry
        </button>
      ) : null}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

export type Tone = "neutral" | "good" | "warn" | "bad" | "info";

export function Badge({ tone = "neutral", children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span className={`badge badge-${tone}`} title={title}>
      {children}
    </span>
  );
}

export function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="field">
      {/* biome-ignore lint/a11y/noLabelWithoutControl: the control is passed as children. */}
      <label className="field-wrap">
        <span className="field-label">{label}</span>
        {children}
      </label>
      {hint ? <span className="field-hint">{hint}</span> : null}
    </div>
  );
}

export function PageHeader({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <header className="page-header">
      <h1>{title}</h1>
      {children ? <div className="page-actions">{children}</div> : null}
    </header>
  );
}

export function Mono({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <code className="mono" title={title}>
      {children}
    </code>
  );
}
