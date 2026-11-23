'use client';

// Who may use a thing: a checkbox for everyone signed in, or named groups
// as pills.
//
// A raw comma-separated string hid its own mistakes -- a trailing comma, a
// doubled entry, a stray "*" that quietly published to the whole access
// point. A pill is a group the form has already accepted, and the "*" is a
// checkbox because it is a different decision rather than another group
// name.
//
// Lifted out of the site-apps page, which is where that reasoning was
// worked out, so the connectors page gets the same control rather than a
// second comma-separated box with the same sharp edges. One implementation:
// the two pages ask the same question of the same group names, and the
// server compares them the same way.

import { useState } from 'react';
import { EVERYONE, parseGroupList } from '@/lib/siteapps';

const INPUT =
  'mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm focus:border-brand-400 focus:outline-hidden focus:ring-1 focus:ring-brand-400';

export function GroupsField({
  label,
  everyoneLabel,
  placeholder = 'smith_lab — press Enter to add',
  testIDPrefix,
  everyone,
  groups,
  pending,
  error,
  onEveryone,
  onGroups,
  onPending,
}: {
  /** The question this field answers, e.g. "Visible to". */
  label: string;
  /** What ticking the box means here, in the page's own words. */
  everyoneLabel: string;
  placeholder?: string;
  /** Prefix for this field's own test ids, so two pages using the control
   *  do not collide in a test that renders both. */
  testIDPrefix: string;
  everyone: boolean;
  groups: string[];
  pending: string;
  error: string | null;
  onEveryone: (v: boolean) => void;
  onGroups: (g: string[]) => void;
  onPending: (v: string) => void;
}) {
  const [note, setNote] = useState<string | null>(null);

  function commit(raw: string) {
    const next = [...groups];
    for (const g of parseGroupList(raw)) {
      if (g === EVERYONE) {
        setNote('“*” is every authenticated user — tick the box above instead.');
        continue;
      }
      if (next.some((h) => h.toLowerCase() === g.toLowerCase())) {
        // Group names are compared case-insensitively by the server, so
        // smith_lab and Smith_Lab are one group, not two.
        continue;
      }
      next.push(g);
      setNote(null);
    }
    onGroups(next);
    onPending('');
  }

  return (
    <div className="text-sm text-gray-700">
      <span className="block">{label}</span>
      <label className="mt-1 flex items-center gap-2">
        <input
          type="checkbox"
          className="rounded-sm border-gray-300"
          data-testid={`${testIDPrefix}-everyone`}
          checked={everyone}
          onChange={(e) => onEveryone(e.target.checked)}
        />
        {everyoneLabel}
      </label>
      {everyone ? null : (
        <>
          {groups.length > 0 ? (
            <div className="mt-2 flex flex-wrap gap-1">
              {groups.map((g) => (
                <span
                  key={g}
                  className="inline-flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-800"
                  data-testid={`group-pill-${g}`}
                >
                  {g}
                  <button
                    type="button"
                    className="text-gray-500 hover:text-gray-900"
                    aria-label={`Remove ${g}`}
                    data-testid={`group-remove-${g}`}
                    onClick={() => onGroups(groups.filter((h) => h !== g))}
                  >
                    ×
                  </button>
                </span>
              ))}
            </div>
          ) : null}
          <input
            className={INPUT}
            placeholder={placeholder}
            data-testid={`${testIDPrefix}-groups`}
            value={pending}
            onChange={(e) => {
              const v = e.target.value;
              // A comma is how the configuration separates them, so typing
              // one finishes the group rather than becoming part of its
              // name.
              if (v.includes(',')) commit(v);
              else onPending(v);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                commit(pending);
              }
            }}
            onBlur={() => {
              if (pending.trim() !== '') commit(pending);
            }}
          />
          {note ? (
            <p className="mt-1 text-xs text-amber-700" data-testid={`${testIDPrefix}-groups-note`}>
              {note}
            </p>
          ) : null}
        </>
      )}
      {error ? (
        <p className="mt-1 text-sm text-red-600" data-testid={`${testIDPrefix}-groups-error`}>
          {error}
        </p>
      ) : null}
    </div>
  );
}
