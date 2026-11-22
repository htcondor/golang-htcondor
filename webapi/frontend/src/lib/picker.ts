// File choosers: picking a file out of a third-party account from the
// submit page.
//
// The server decides which choosers exist and whether this caller may
// use one (/api/v1/pickers); this module runs one and turns what comes
// back into a submit-file reference.
//
// The chooser itself is the provider's code, loaded from the provider's
// origin, and it is not imported here. It arrives through an adapter
// served from our own origin which implements one narrow contract:
//
//   window.htcondorPicker.open({ token, apiKey, appId,
//                                onPicked, onCancel, onError })
//
// That indirection is what makes this testable. A test serves a
// stand-in adapter implementing the same contract, and the page code
// under test is then the page code that ships -- rather than a second
// path that only ever tests itself.

export interface Picker {
  name: string;
  provider: string;
  /** The connector whose credential this chooser borrows, "ns:name". */
  connector: string;
  /** The credential half of a reference. */
  service: string;
  /** The transfer method half. */
  method: string;
  /** Where to load the adapter from. */
  script: string;
  /**
   * The provider's developer key, not the credential's. Public by
   * design: it lives in client-side JavaScript either way.
   */
  api_key?: string;
  app_id?: string;
}

export interface PickedFile {
  id: string;
  name: string;
}

/** The adapter contract, as implemented by webapi/httpserver/pickerjs. */
export interface PickerAdapter {
  open(opts: {
    token: string;
    apiKey?: string;
    appId?: string;
    onPicked: (files: PickedFile[]) => void;
    onCancel?: () => void;
    onError?: (message: string) => void;
  }): void;
}

declare global {
  interface Window {
    htcondorPicker?: PickerAdapter;
  }
}

/**
 * A token answer. Not connected is an ordinary outcome and not an
 * error: the first time anybody uses a chooser they have not consented
 * yet, and the useful response is somewhere to go.
 */
export type TokenOutcome =
  | { connected: true; token: string }
  | { connected: false; connectUrl: string; needsConsent: boolean };

/** The choosers this caller can use. */
export async function fetchPickers(): Promise<Picker[]> {
  const res = await fetch("/api/v1/pickers", {
    credentials: "include",
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    // A chooser is an extra, not the page's purpose. An access point
    // with none configured answers 404 for the route, and that is not
    // a failure to report to somebody trying to submit a job.
    return [];
  }
  const body = await res.json();
  return Array.isArray(body) ? body : [];
}

/**
 * A short-lived access token for a connector, for this browser only.
 *
 * 409 means not connected, with somewhere to go; the server
 * distinguishes never-consented from a grant that has since died, and
 * both land here as connected: false.
 */
export async function fetchConnectorToken(
  connector: string,
): Promise<TokenOutcome> {
  const res = await fetch(
    `/api/v1/connect/${encodeURIComponent(connector)}/token`,
    { credentials: "include", headers: { Accept: "application/json" } },
  );
  if (res.status === 409) {
    const body = await res.json().catch(() => ({}));
    return {
      connected: false,
      connectUrl: body.connect_url ?? `/api/v1/connect/${connector}`,
      needsConsent: body.needs_consent === true,
    };
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.message || body.error || `HTTP ${res.status}`);
  }
  const body = await res.json();
  if (!body.access_token) {
    throw new Error("the access point returned no token");
  }
  return { connected: true, token: body.access_token };
}

/**
 * The submit-file reference for a chosen file.
 *
 * Composed from the service and method the server sent rather than
 * spelled here, so this cannot drift from what the server's parser
 * accepts: if the composition changes, it changes in the fields.
 */
export function fileReference(p: Picker, f: PickedFile): string {
  return `${p.service}+${p.method}://${f.id}/${f.name}`;
}

/**
 * What stops a file's name being usable as a reference, or null.
 *
 * This is the one rule that CANNOT live only on the server, which is
 * why it is duplicated here rather than being asked for. A name with a
 * space or a comma produces a reference that the server reads as a
 * shorter reference plus a second input file -- correctly, because that
 * is what those tokens mean when a submitter types them on purpose.
 * Nothing in the text separates the two intents, so the server cannot
 * refuse it, and the only place with the real name in hand is here.
 *
 * The rest of the rule mirrors CheckSpoolName in connectors/spool.go,
 * which remains the authority: if these disagree, the server refuses
 * the submission and this message was merely early. The point of
 * saying it here is that a person learns at the moment they pick the
 * file, with the chooser still in front of them, rather than at submit.
 */
export function nameProblem(name: string): string | null {
  if (name === "" ) return "has no name";
  if (name === "." || name === "..") {
    return "is named like a directory reference, which cannot be a file in a job's sandbox";
  }
  if (name.includes("/") || name.includes("\\")) {
    return "has a path separator in its name";
  }
  for (const ch of name) {
    const c = ch.codePointAt(0)!;
    if (c < 0x20 || c === 0x7f) {
      return "has a control character in its name";
    }
  }
  // A submit file splits transfer_input_files on these and expands
  // macros, so none of them can be part of a name.
  const reserved = [
    [" ", "a space"],
    ["\t", "a tab"],
    [",", "a comma"],
    [";", "a semicolon"],
    ['"', "a quote"],
    ["'", "an apostrophe"],
    ["$", "a dollar sign"],
  ] as const;
  for (const [ch, described] of reserved) {
    if (name.includes(ch)) {
      return `has ${described} in its name, which a submit file reads as something other than part of a name`;
    }
  }
  return null;
}

/** Split what a chooser returned into what can be used and what cannot. */
export function partitionPicked(files: PickedFile[]): {
  usable: PickedFile[];
  rejected: { file: PickedFile; problem: string }[];
} {
  const usable: PickedFile[] = [];
  const rejected: { file: PickedFile; problem: string }[] = [];
  for (const f of files) {
    const problem = nameProblem(f.name);
    if (problem) rejected.push({ file: f, problem });
    else usable.push(f);
  }
  return { usable, rejected };
}

// Adapters already loaded, by script URL. A second open must not add a
// second <script>, and must not read window.htcondorPicker after
// another provider's adapter has overwritten it.
const adapters = new Map<string, Promise<PickerAdapter>>();

/** Load a chooser's adapter and return its implementation. */
export function loadPickerAdapter(src: string): Promise<PickerAdapter> {
  const cached = adapters.get(src);
  if (cached) return cached;

  const loading = new Promise<PickerAdapter>((resolve, reject) => {
    const el = document.createElement("script");
    el.src = src;
    el.async = true;
    el.dataset.htcondorPickerAdapter = src;
    el.addEventListener("load", () => {
      const adapter = window.htcondorPicker;
      if (!adapter || typeof adapter.open !== "function") {
        reject(
          new Error(
            "the file chooser's adapter loaded but defined no chooser",
          ),
        );
        return;
      }
      resolve(adapter);
    });
    // A blocked load and a missing file look the same from here, and
    // the only other evidence is a console entry nobody reporting the
    // problem will think to look at.
    el.addEventListener("error", () =>
      reject(
        new Error(
          `could not load the file chooser from ${src}. If this access point's ` +
            `Content-Security-Policy does not name that origin, the browser ` +
            `refuses it before any request is made.`,
        ),
      ),
    );
    document.head.appendChild(el);
  });

  // A failure is not cached: a chooser that failed once because the
  // network was down should be openable again.
  adapters.set(
    src,
    loading.catch((err) => {
      adapters.delete(src);
      throw err;
    }),
  );
  return adapters.get(src)!;
}

/** What a run of the chooser produced. */
export type ChooseOutcome =
  | { kind: "picked"; files: PickedFile[] }
  | { kind: "cancelled" }
  | { kind: "needs-connect"; connectUrl: string; needsConsent: boolean };

/**
 * Open a chooser and resolve with what the person picked.
 *
 * Rejects only on something that went wrong. Cancelling is an outcome,
 * not an error, and so is not being connected yet.
 */
export async function chooseFiles(p: Picker): Promise<ChooseOutcome> {
  const token = await fetchConnectorToken(p.connector);
  if (!token.connected) {
    return {
      kind: "needs-connect",
      connectUrl: token.connectUrl,
      needsConsent: token.needsConsent,
    };
  }
  const adapter = await loadPickerAdapter(p.script);
  // A chooser may call back more than once -- Google's fires one per
  // action, so a CANCEL can follow a PICKED -- and the first outcome
  // has to be the one that counts. No guard is needed for that: a
  // settled Promise ignores every later resolve and reject. A guard
  // here was removed because no test could tell it from its absence,
  // which is the same as saying it did nothing. The property itself is
  // held by a test, since a reader has no reason to assume it.
  return new Promise<ChooseOutcome>((resolve, reject) => {
    adapter.open({
      token: token.token,
      apiKey: p.api_key,
      appId: p.app_id,
      onPicked: (files) => resolve({ kind: "picked", files: files ?? [] }),
      onCancel: () => resolve({ kind: "cancelled" }),
      onError: (message) => reject(new Error(message)),
    });
  });
}
