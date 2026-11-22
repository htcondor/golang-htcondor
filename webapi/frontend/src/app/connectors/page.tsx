"use client";

// Connectors: the services this account is wired to, in both directions.
//
// The two directions are genuinely different things and the page says so,
// because the trust runs opposite ways:
//
//   - Apps connected to your account (below). An outside service holds a
//     credential that lets it act as you HERE. You are the authorization
//     server; it is the client.
//   - Services you connected. You grant this server access to YOUR account
//     somewhere else -- Drive and the like -- so a job can read or write
//     files there. That half lives in the connectors package and is not on
//     this page yet; it slots in as a second section beside this one.
//
// Mixing them under one heading without that distinction is how somebody
// revokes the wrong thing, so each section names who ends up able to do what.

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type Integration,
  type IntegrationCreated,
  type IntegrationEndpoints,
} from "@/lib/api";
import { ChipList } from "@/components/ChipList";
import { ConfirmButton } from "@/components/ConfirmButton";

// The scopes worth offering as checkboxes. The server narrows whatever is
// asked for to what the account could consent to, so an unavailable one here
// is dropped rather than refused -- which is why the created client's real
// scope list is read back from the server afterwards and not assumed.
const OFFERED_SCOPES: { scope: string; label: string; hint: string }[] = [
  { scope: "mcp:read", label: "Read your jobs", hint: "Queue, history, pool status." },
  { scope: "mcp:write", label: "Act on your jobs", hint: "Submit, hold, release, remove." },
];

// offline_access is not a permission over anything -- it asks for a refresh
// token -- and without it an unattended integration stops working the first
// time its access token expires. It is not offered as a choice because
// declining it has no upside and one sharp edge.
const ALWAYS_SCOPES = ["openid", "offline_access"];

export default function ConnectorsPage() {
  const qc = useQueryClient();
  const [minted, setMinted] = useState<IntegrationCreated | null>(null);

  const { data, isLoading, error } = useQuery({
    queryKey: ["integrations"],
    queryFn: api.integrations.list,
  });

  const create = useMutation({
    mutationFn: api.integrations.create,
    onSuccess: (created) => {
      setMinted(created);
      qc.invalidateQueries({ queryKey: ["integrations"] });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.integrations.remove(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["integrations"] }),
  });

  const integrations = data?.integrations ?? [];

  return (
    <div className="max-w-5xl space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">Connectors</h1>
        <p className="mt-1 text-sm text-gray-500">
          Services this account is wired to. Read each section&apos;s heading
          for which way the access runs.
        </p>
      </div>

      <section className="space-y-4">
        <div>
          <h2 className="text-lg font-semibold text-gray-900">
            Apps connected to your account
          </h2>
          <p className="mt-1 text-sm text-gray-500">
            An app you connect here can act as you on this access point, within
            the permissions you grant it when you sign in. Use this for a
            service that asks you to paste a client ID and secret. Anything
            that supports automatic setup does not need an entry here.
          </p>
        </div>

        {minted && (
          <NewClientShown
            minted={minted}
            onDismiss={() => setMinted(null)}
          />
        )}

        <CreateForm
          pending={create.isPending}
          error={create.error}
          onCreate={(body) => create.mutate(body)}
        />

        {isLoading && <p className="text-sm text-gray-500">Loading…</p>}
        {error && (
          <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {(error as Error).message}
          </p>
        )}

        {!isLoading && !error && integrations.length === 0 && (
          <p className="rounded-lg border border-dashed border-gray-300 px-4 py-6 text-center text-sm text-gray-500">
            No apps are connected to your account.
          </p>
        )}

        {integrations.length > 0 && (
          <IntegrationTable
            integrations={integrations}
            endpoints={data?.endpoints}
            onRemove={(id) => remove.mutate(id)}
            removing={remove.isPending}
          />
        )}
      </section>
    </div>
  );
}

// CreateForm collects the little an app needs. Everything else is defaulted
// by the server, which is also what enforces the limits, so this form does
// not re-implement them -- it only avoids asking for what cannot vary.
function CreateForm({
  onCreate,
  pending,
  error,
}: {
  onCreate: (body: {
    name: string;
    redirect_uris?: string[];
    scopes?: string[];
    public?: boolean;
  }) => void;
  pending: boolean;
  error: unknown;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [redirect, setRedirect] = useState("");
  const [scopes, setScopes] = useState<string[]>(["mcp:read"]);
  const [isPublic, setIsPublic] = useState(false);

  const toggle = (scope: string) =>
    setScopes((cur) =>
      cur.includes(scope) ? cur.filter((s) => s !== scope) : [...cur, scope],
    );

  const submit = () => {
    onCreate({
      name: name.trim(),
      redirect_uris: redirect.trim() ? [redirect.trim()] : undefined,
      scopes: [...ALWAYS_SCOPES, ...scopes],
      public: isPublic,
    });
    setName("");
    setRedirect("");
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700"
      >
        Connect an app
      </button>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border border-gray-200 bg-gray-50 p-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block">
          <span className="text-xs font-medium text-gray-700">Name</span>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Zapier"
            className="mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm text-gray-900"
          />
          <span className="mt-1 block text-xs text-gray-500">
            So you can tell your connected apps apart.
          </span>
        </label>
        <label className="block">
          <span className="text-xs font-medium text-gray-700">
            Redirect URL
          </span>
          <input
            type="text"
            value={redirect}
            onChange={(e) => setRedirect(e.target.value)}
            placeholder="https://hooks.zapier.com/…"
            className="mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm text-gray-900"
          />
          <span className="mt-1 block text-xs text-gray-500">
            The app gives you this. Must be https, or http on localhost.
          </span>
        </label>
      </div>

      <fieldset>
        <legend className="text-xs font-medium text-gray-700">
          What the app may do as you
        </legend>
        <div className="mt-1 space-y-1">
          {OFFERED_SCOPES.map((s) => (
            <label key={s.scope} className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={scopes.includes(s.scope)}
                onChange={() => toggle(s.scope)}
                className="mt-1"
              />
              <span>
                <span className="text-gray-900">{s.label}</span>{" "}
                <span className="text-xs text-gray-500">{s.hint}</span>
              </span>
            </label>
          ))}
        </div>
        <p className="mt-1 text-xs text-gray-500">
          You confirm these again when you sign the app in, and you can grant
          less there. Anything your account is not entitled to is dropped.
        </p>
      </fieldset>

      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={isPublic}
          onChange={(e) => setIsPublic(e.target.checked)}
          className="mt-1"
        />
        <span>
          <span className="text-gray-900">
            This app cannot keep a secret
          </span>{" "}
          <span className="text-xs text-gray-500">
            For a command-line or desktop app. No client secret is issued.
          </span>
        </span>
      </label>

      {error != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(error as Error).message}
        </p>
      )}

      <div className="flex gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={pending || name.trim() === ""}
          className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {pending ? "Creating…" : "Create"}
        </button>
        <button
          type="button"
          onClick={() => setOpen(false)}
          className="rounded-sm border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

// NewClientShown is the shown-once display, and it shows everything the
// app's setup form will ask for -- not just the secret -- because that is the
// moment the user is going to paste it all somewhere.
//
// Dismissed by hand rather than on a timer, the way the API key page does it:
// a user who fumbles the copy can read it again until they say they are done.
function NewClientShown({
  minted,
  onDismiss,
}: {
  minted: IntegrationCreated;
  onDismiss: () => void;
}) {
  return (
    <div className="space-y-3 rounded-lg border border-amber-300 bg-amber-50 p-4">
      <div>
        <h3 className="text-sm font-semibold text-amber-900">
          {minted.name ? `"${minted.name}" is ready` : "App connected"}
        </h3>
        <p className="mt-1 text-xs text-amber-800">
          {minted.client_secret
            ? "Copy the secret now. It is never shown again — losing it means deleting this app and connecting it afresh."
            : "This app was created without a secret, as you asked."}
        </p>
      </div>

      <CopyField label="Client ID" value={minted.id} mono />
      {minted.client_secret && (
        <CopyField label="Client secret" value={minted.client_secret} mono />
      )}
      {minted.endpoints && (
        <>
          <CopyField
            label="Authorization URL"
            value={minted.endpoints.authorization_endpoint}
          />
          <CopyField
            label="Token URL"
            value={minted.endpoints.token_endpoint}
          />
        </>
      )}

      <button
        type="button"
        onClick={onDismiss}
        className="rounded-sm bg-amber-600 px-3 py-1 text-xs font-medium text-white hover:bg-amber-700"
      >
        I&apos;ve saved it
      </button>
    </div>
  );
}

// CopyField is a readonly value with a copy button. The input stays
// selectable so a browser that refuses the clipboard API still leaves the
// value reachable by hand.
function CopyField({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Refused on an insecure origin; the input is still selectable.
    }
  };
  return (
    <label className="block">
      <span className="text-xs font-medium text-amber-900">{label}</span>
      <span className="mt-1 flex gap-2">
        <input
          type="text"
          readOnly
          value={value}
          onFocus={(e) => e.currentTarget.select()}
          className={`flex-1 rounded-sm border border-amber-300 bg-white px-2 py-1 text-xs text-gray-900 ${
            mono ? "font-mono" : ""
          }`}
        />
        <button
          type="button"
          onClick={copy}
          className="rounded-sm border border-amber-400 bg-white px-3 py-1 text-xs font-medium text-amber-900 hover:bg-amber-100"
        >
          {copied ? "Copied!" : "Copy"}
        </button>
      </span>
    </label>
  );
}

function IntegrationTable({
  integrations,
  endpoints,
  onRemove,
  removing,
}: {
  integrations: Integration[];
  endpoints?: IntegrationEndpoints;
  onRemove: (id: string) => void;
  removing: boolean;
}) {
  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-lg border border-gray-200">
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50">
            <tr>
              <th className="px-3 py-2 text-left font-medium text-gray-700">
                App
              </th>
              <th className="px-3 py-2 text-left font-medium text-gray-700">
                May do as you
              </th>
              <th className="px-3 py-2 text-left font-medium text-gray-700">
                Last used
              </th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 bg-white">
            {integrations.map((it) => (
              <tr key={it.id}>
                <td className="px-3 py-2">
                  <div className="text-gray-900">{it.name || it.id}</div>
                  <div className="font-mono text-xs text-gray-500">{it.id}</div>
                  {!it.has_secret && (
                    <div className="text-xs text-gray-500">No secret</div>
                  )}
                </td>
                <td className="px-3 py-2">
                  {/* The scopes the client may ASK for. What a sign-in
                      actually granted is on the OAuth2 tokens page. */}
                  <ChipList items={it.scopes} max={4} />
                </td>
                <td className="px-3 py-2 text-gray-700">
                  {it.last_used_at
                    ? new Date(it.last_used_at).toLocaleString()
                    : "Never"}
                </td>
                <td className="px-3 py-2 text-right">
                  <ConfirmButton
                    label="Delete"
                    title="Delete this app and every token it holds"
                    compact
                    pending={removing}
                    onConfirm={() => onRemove(it.id)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {endpoints && (
        <details className="rounded-lg border border-gray-200 bg-gray-50 p-3 text-sm">
          <summary className="cursor-pointer font-medium text-gray-900">
            URLs an app&apos;s setup form asks for
          </summary>
          <dl className="mt-2 space-y-1 text-xs">
            <div>
              <dt className="inline font-medium text-gray-700">
                Authorization URL:{" "}
              </dt>
              <dd className="inline font-mono text-gray-600">
                {endpoints.authorization_endpoint}
              </dd>
            </div>
            <div>
              <dt className="inline font-medium text-gray-700">Token URL: </dt>
              <dd className="inline font-mono text-gray-600">
                {endpoints.token_endpoint}
              </dd>
            </div>
          </dl>
          <p className="mt-2 text-xs text-gray-500">
            Deleting an app here stops it immediately: its tokens go with it.
          </p>
        </details>
      )}
    </div>
  );
}
