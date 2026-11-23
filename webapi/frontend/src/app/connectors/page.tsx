"use client";

// Connectors: the services this account is wired to, in both directions.
//
// The two directions are genuinely different things and the page says so,
// because the trust runs opposite ways:
//
//   - Apps connected to your account. An outside service holds a credential
//     that lets it act as you HERE. You are the authorization server; it is
//     the client.
//   - Services you connected. You grant this server access to YOUR account
//     somewhere else -- Drive and the like -- so a job can read or write
//     files there. We hold the credential, which is why that section
//     disconnects rather than deletes.
//   - Your own machines over SSH. A certificate your laptop holds that
//     lets it act as you here. The same direction as the first section
//     -- something outside holding a credential for this account -- and
//     a section of its own because the holder is not an app: nothing
//     consented on your behalf, there is no client and no scope screen,
//     and what was granted is a shell.
//   - Event notifications. This server posts to a service when your jobs
//     change state. Nothing outside gains any access; we reach out to it.
//
// The two OAuth2 sections are adjacent because setting one up is the same
// job either way, and somebody who has just been told to "connect an app"
// should not have to scroll past the webhooks to find out which of the two
// they meant.
//
// They are not ONE table. The access runs opposite ways, and the columns and
// the verbs differ with it -- permissions against connection state, Delete
// against Disconnect. Merging them would turn the direction into a column
// value to skim past, and revoking the wrong thing is the mistake this page
// exists to prevent.

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type Integration,
  type IntegrationCreated,
  type IntegrationEndpoints,
} from "@/lib/api";
import { ConfirmButton } from "@/components/ConfirmButton";
import { ScopeChips } from "@/components/ScopeChips";
import { WebhooksSection } from "./WebhooksSection";
import { ConnectedServicesSection } from "./ConnectedServicesSection";
import { GrantsSection } from "./GrantsSection";
import { SSHSection } from "./SSHSection";

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

// One header treatment for every table on this page and the admin pages:
// an operator moving between /connectors and /admin/tokens should not have
// to re-read a table to find out it is the same kind of thing.
const TH = "px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-500";

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
          {/* The section as a whole, now that it holds two lists. What
              is specific to REGISTERING one moved down to the heading
              that introduces the form, because it was reading as a
              description of the access list above it. */}
          <p className="mt-1 text-sm text-gray-500">
            An app connected here can act as you on this access point, within the
            permissions you grant it when you sign in. Nothing here can see other
            people&apos;s jobs unless you granted it a permission that says so.
          </p>
        </div>

        {/* What is holding access right now, above the clients this
            person registered. Same direction -- an app acting as you here
            -- so it belongs in this section and not under "Services you
            connected", which is the opposite arrow. It comes first
            because "what can get at my account?" is the question somebody
            opens this page with, and an app that registered itself
            appears only here. */}
        <GrantsSection />

        {minted && (
          <NewClientShown
            minted={minted}
            onDismiss={() => setMinted(null)}
          />
        )}

        <div className="pt-2">
          <h3 className="text-sm font-semibold text-gray-900">Apps you registered here</h3>
          <p className="mt-1 text-sm text-gray-500">
            Register one for a service that asks you to paste a client ID and secret.
            Anything that sets itself up does not need an entry here — it appears above
            once you connect it, which is why the two lists are not the same.
          </p>
        </div>

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

      <hr className="border-gray-200" />

      {/* Directly after the apps, because it is the same arrow: a
          credential held OUTSIDE that acts as you HERE. It moved off
          /info, which is a readout -- version, uptime, your groups --
          and was the one thing on that page that did something.

          Renders nothing where the deployment has no SSH certificate
          authority, which is a statement about the installation rather
          than a fault. */}
      <SSHSection />

      <hr className="border-gray-200" />

      {/* Next to the section above, not after the webhooks, because the
          two are the same job: wiring an OAuth2 app up. They stay two
          tables rather than one because the access runs OPPOSITE ways --
          above, an app acts as you here; here, this server acts as you
          somewhere else -- and the columns and the verbs differ with it
          (permissions vs connection state, Delete vs Disconnect). One
          table would make the direction a column value to skim past,
          which is exactly the confusion this page exists to prevent.

          Renders nothing where the deployment has no connectors, or in
          multi-AP mode, which does not serve their endpoints. */}
      <ConnectedServicesSection />

      <hr className="border-gray-200" />

      <WebhooksSection />
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

// RedirectHint shows where an app is sent back to after a sign-in.
//
// Truncated rather than redacted. A redirect URL is not a capability: the
// user typed it, it is sent in the clear in every authorization request, and
// the consent screen shows the app it belongs to. What makes it worth
// showing is the host -- "zapier.com" is the only thing on the row that
// identifies the service -- so the host is never the part that gets cut.
function RedirectHint({ uris }: { uris?: string[] }) {
  if (!uris || uris.length === 0) {
    // A client registered with no redirect URL is a device-code or
    // client-credentials client. Say so rather than leaving a gap that
    // reads as a missing value.
    return (
      <div className="text-xs text-gray-500">No sign-in redirect</div>
    );
  }
  const [first, ...rest] = uris;
  let host = "";
  try {
    host = new URL(first).host;
  } catch {
    // Not parseable: show it as given rather than hiding it.
  }
  return (
    <div className="text-xs text-gray-600" title={uris.join("\n")}>
      <span className="font-medium">{host || first}</span>
      {host && (
        <span className="ml-1 font-mono text-gray-500">
          {truncatePath(first)}
        </span>
      )}
      {rest.length > 0 && (
        <span className="ml-1 text-gray-500">+{rest.length} more</span>
      )}
    </div>
  );
}

// truncatePath keeps a redirect readable in a table cell: enough of the
// path to tell two apps on the same host apart, and no more.
function truncatePath(raw: string): string {
  let path = "";
  try {
    const u = new URL(raw);
    path = u.pathname + u.search;
  } catch {
    return "";
  }
  if (path === "/" || path === "") return "";
  return path.length > 36 ? path.slice(0, 36) + "…" : path;
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
              <th className={TH}>App</th>
              <th className={TH}>May ask for</th>
              <th className={TH}>Last used</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 bg-white">
            {integrations.map((it) => (
              <tr key={it.id}>
                <td className="px-3 py-2">
                  <div className="text-gray-900">{it.name || it.id}</div>
                  {/* The redirect URL, because it is what says WHICH
                      service this is. A name is whatever somebody typed
                      -- "inbound-test" could be anything -- and the
                      client id says nothing at all; the host in the
                      redirect is the one field that reads as
                      "zapier.com". It is not a secret: the user supplied
                      it, it travels in every authorization request, and
                      it is shown on the consent screen. */}
                  <RedirectHint uris={it.redirect_uris} />
                  <div className="font-mono text-xs text-gray-400">{it.id}</div>
                  {!it.has_secret && (
                    <div className="text-xs text-gray-500">No secret</div>
                  )}
                </td>
                <td className="px-3 py-2">
                  {/* The scopes the client may ASK for. What a sign-in
                      actually GRANTED is in "Holding access right now"
                      above, which is the narrower set whenever the server
                      refused something. */}
                  <ScopeChips scopes={it.scopes} />
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
