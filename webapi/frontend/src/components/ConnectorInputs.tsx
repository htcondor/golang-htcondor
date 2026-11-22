'use client';

// "Add from Drive": picking a job's input out of a third-party account.
//
// A chosen file is not an upload. Nothing is read in the browser and
// nothing is attached to the submission: what goes into the submit file
// is a reference, and the access point fetches the bytes itself on the
// way through. So these sit beside the dropzone rather than in it, and
// the list below shows the reference, because that is the thing that
// ends up in transfer_input_files.

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ensureTransferInput } from '@/lib/submitFile';
import {
  chooseFiles,
  fetchPickers,
  fileReference,
  partitionPicked,
  type Picker,
} from '@/lib/picker';
import { providerNames } from '@/lib/connectorNames';

/** One chosen file, as the submit page holds it. */
export interface ConnectorInput {
  /** Stable key for the list. */
  id: string;
  /** The submit-file reference. */
  reference: string;
  /** The name it will land under in the sandbox. */
  name: string;
  /** Which chooser it came from, for the label. */
  picker: string;
}

/** A file whose name the submit file cannot carry. */
interface Refused {
  name: string;
  problem: string;
}

let seq = 0;
function nextID(): string {
  seq += 1;
  return `conn-${seq}`;
}

export function ConnectorInputs({
  value,
  onChange,
  disabled,
}: {
  value: ConnectorInput[];
  onChange: (next: ConnectorInput[]) => void;
  disabled?: boolean;
}) {
  // An access point with no chooser configured answers with an empty
  // list (or no route at all), and this renders nothing. Somebody
  // submitting a job should not have to read about a feature their
  // access point does not offer.
  const pickers = useQuery({
    queryKey: ['pickers'],
    queryFn: fetchPickers,
    staleTime: 5 * 60 * 1000,
  });

  const list = pickers.data ?? [];

  const [busy, setBusy] = useState<string | null>(null);
  // Separate from `busy` because the two say different things to the
  // person waiting: the chooser is being fetched, or it is up and the
  // next move is theirs.
  const [choosing, setChoosing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refused, setRefused] = useState<Refused[]>([]);
  const [connect, setConnect] = useState<{
    picker: Picker;
    url: string;
    needsConsent: boolean;
  } | null>(null);

  const open = async (p: Picker) => {
    setError(null);
    setRefused([]);
    setConnect(null);
    setBusy(p.name);
    setChoosing(false);
    try {
      const out = await chooseFiles(p, { onOpened: () => setChoosing(true) });
      if (out.kind === 'cancelled') return;
      if (out.kind === 'needs-connect') {
        setConnect({
          picker: p,
          url: out.connectUrl,
          needsConsent: out.needsConsent,
        });
        return;
      }
      // A name the submit file cannot carry is reported per file, and
      // the rest are still added: picking three files and being told
      // "no" about all of them because one is called "my data.csv"
      // loses the two choices that were fine.
      const { usable, rejected } = partitionPicked(out.files);
      setRefused(rejected.map((r) => ({ name: r.file.name, problem: r.problem })));

      const have = new Set(value.map((v) => v.reference));
      const add: ConnectorInput[] = [];
      for (const f of usable) {
        const reference = fileReference(p, f);
        if (have.has(reference)) continue;
        have.add(reference);
        add.push({ id: nextID(), reference, name: f.name, picker: p.name });
      }
      if (add.length > 0) onChange([...value, ...add]);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
      setChoosing(false);
    }
  };

  if (list.length === 0 && value.length === 0) return null;

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        {list.map((p) => (
          <button
            key={p.name}
            type="button"
            disabled={disabled || busy !== null}
            onClick={() => void open(p)}
            data-testid={`picker-open-${p.name}`}
            className="rounded-sm border border-gray-300 bg-white px-3 py-1.5 hover:bg-gray-100 disabled:opacity-50"
          >
            {busy === p.name
              ? choosing
                ? 'Choosing…'
                : 'Opening…'
              : `Add from ${labelFor(p, list)}…`}
          </button>
        ))}
        {list.length > 0 && (
          <span className="text-gray-500">
            Fetched by the access point when the job is submitted — nothing
            is uploaded from this browser.
          </span>
        )}
      </div>

      {connect && (
        <p
          className="text-xs text-amber-800"
          data-testid="picker-needs-connect"
        >
          {connect.needsConsent
            ? `Your ${labelFor(connect.picker, list)} authorization is no longer valid.`
            : `This access point has no ${labelFor(connect.picker, list)} authorization from you yet.`}{' '}
          <a className="underline" href={connect.url}>
            Authorize it
          </a>
          , then pick the file again.
        </p>
      )}

      {error && (
        <p className="text-xs text-red-700" data-testid="picker-error">
          {error}
        </p>
      )}

      {refused.length > 0 && (
        <ul className="text-xs text-red-700" data-testid="picker-refused">
          {refused.map((r) => (
            <li key={r.name}>
              <span className="font-mono">{r.name}</span> {r.problem}. Rename
              it where it lives and pick it again.
            </li>
          ))}
        </ul>
      )}

      {value.length > 0 && (
        <ul
          className="rounded-sm border border-gray-200 bg-white divide-y divide-gray-100 text-sm"
          data-testid="picker-chosen"
        >
          {value.map((v) => (
            <li key={v.id} className="flex items-center gap-2 px-3 py-2">
              <span className="min-w-0 flex-1">
                <span className="font-mono text-xs">{v.name}</span>
                {/* The reference is shown because it is what the submit
                    file will contain, and because the file id in it is
                    the only way to tell two files of the same name
                    apart. */}
                <span className="block truncate font-mono text-[11px] text-gray-500">
                  {v.reference}
                </span>
              </span>
              <button
                type="button"
                disabled={disabled}
                onClick={() => onChange(value.filter((x) => x.id !== v.id))}
                aria-label={`Remove ${v.name}`}
                className="shrink-0 text-xs text-gray-500 hover:text-red-700 disabled:opacity-50"
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * What to call a chooser in a button.
 *
 * The provider is what a person recognizes; the configured name is the
 * operator's and may be anything ("drive", "lab-shared"), so leading
 * with it gives a button reading "Add from drive…". The name is added
 * only where it has to be: an access point with two choosers for one
 * provider needs them told apart, and one with a single Drive chooser
 * does not need its internal name on a button.
 */
function labelFor(p: Picker, all: Picker[]): string {
  const friendly = providerNames[p.provider] ?? p.provider;
  const sameProvider = all.filter((o) => o.provider === p.provider).length;
  if (sameProvider > 1) return `${friendly} (${p.name})`;
  return friendly;
}

/**
 * Put the chosen files into a submit body.
 *
 * Appends each reference to transfer_input_files, which is the whole
 * mechanism: the access point's submit path finds them there, fetches
 * them, and rewrites each reference to the name it fetched it under.
 */
export function applyConnectorInputs(
  body: string,
  inputs: ConnectorInput[],
): string {
  let out = body;
  for (const input of inputs) {
    out = ensureTransferInput(out, input.reference);
  }
  return out;
}
