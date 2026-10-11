// SPDX-License-Identifier: GPL-3.0-or-later

import { type KeyboardEvent, useId, useMemo, useState } from "react";

export type Option = {
  value: string;
  label: string;
  /** Shown muted on the right, e.g. an entry count. */
  detail?: string;
};

type Props = {
  label: string;
  /** Selected values. */
  values: string[];
  onChange: (values: string[]) => void;
  /** Suggestions for the current text. Selected ones are left out. */
  options: Option[];
  /** Called as the user types, for suggestions fetched from the server. */
  onQuery?: (text: string) => void;
  /** Label of a selected value whose option isn't loaded. */
  labelFor?: (value: string) => string;
  placeholder?: string;
  hint?: string;
  /** Filters the options by the typed text here (when they aren't fetched per query). */
  filterLocally?: boolean;
  className?: string;
};

/**
 * A multi-select with type-ahead: chosen values are chips, typing narrows the
 * suggestions, arrows and Enter pick one, Backspace on an empty field removes
 * the last chip. It follows the ARIA combobox pattern.
 */
export function MultiSelect({
  label,
  values,
  onChange,
  options,
  onQuery,
  labelFor,
  placeholder,
  hint,
  filterLocally,
  className,
}: Props) {
  const id = useId();
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);

  const known = useMemo(() => new Map(options.map((o) => [o.value, o.label])), [options]);
  const nameOf = (v: string) => known.get(v) ?? labelFor?.(v) ?? v;

  const shown = useMemo(() => {
    const free = options.filter((o) => !values.includes(o.value));
    const t = text.trim().toLowerCase();
    return (filterLocally && t ? free.filter((o) => o.label.toLowerCase().includes(t)) : free).slice(0, 50);
  }, [options, values, text, filterLocally]);

  const pick = (o: Option) => {
    onChange([...values, o.value]);
    setText("");
    onQuery?.("");
    setActive(0);
  };

  const onKey = (event: KeyboardEvent<HTMLInputElement>) => {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setOpen(true);
        setActive((a) => Math.min(a + 1, Math.max(shown.length - 1, 0)));
        break;
      case "ArrowUp":
        event.preventDefault();
        setActive((a) => Math.max(a - 1, 0));
        break;
      case "Enter": {
        const o = shown[active];
        if (open && o) {
          event.preventDefault();
          pick(o);
        }
        break;
      }
      case "Escape":
        setOpen(false);
        break;
      case "Backspace":
        if (text === "" && values.length > 0) onChange(values.slice(0, -1));
        break;
    }
  };

  const listId = `${id}-list`;
  const expanded = open && shown.length > 0;
  return (
    <div className={`field ${className ?? ""}`}>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <div className="multi-wrap">
        <div className="multi">
          {values.map((v) => (
            <span key={v} className="chip">
              {nameOf(v)}
              <button
                type="button"
                aria-label={`Remove ${nameOf(v)}`}
                onClick={() => onChange(values.filter((x) => x !== v))}
              >
                ×
              </button>
            </span>
          ))}
          <input
            id={id}
            type="text"
            role="combobox"
            aria-expanded={expanded}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={expanded ? `${listId}-${active}` : undefined}
            autoComplete="off"
            value={text}
            placeholder={values.length === 0 ? placeholder : undefined}
            onChange={(e) => {
              setText(e.target.value);
              setOpen(true);
              setActive(0);
              onQuery?.(e.target.value);
            }}
            onFocus={() => setOpen(true)}
            onBlur={() => setOpen(false)}
            onKeyDown={onKey}
          />
        </div>
        {expanded ? (
          <div className="listbox" id={listId} role="listbox" aria-label={label}>
            {shown.map((o, i) => (
              <div
                key={o.value}
                tabIndex={-1}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                // mousedown, so the input doesn't blur first and close the list.
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(o);
                }}
                onMouseEnter={() => setActive(i)}
              >
                <span>{o.label}</span>
                {o.detail ? <span className="detail">{o.detail}</span> : null}
              </div>
            ))}
          </div>
        ) : null}
      </div>
      {hint ? <span className="field-hint">{hint}</span> : null}
    </div>
  );
}
