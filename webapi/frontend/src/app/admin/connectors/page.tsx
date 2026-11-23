"use client";

// Connectors: accounts this access point connects TO on a user's
// behalf, so a job can use a credential the user granted.
//
// NOT the same thing as OAuth2 Clients, one entry up in this menu.
// Those are applications connecting to this server's identity provider
// -- traffic coming in. These go out. The two are a sentence apart in a
// hurried reading, so the page says which it is rather than relying on
// its title.
//
// A connector may be declared two ways and the configuration wins. The
// page therefore leads with provenance, the way the site-apps page
// does, because that is the difference between a change that takes
// effect now and one that needs condor_reconfig. A STORED row the
// configuration also declares is shown struck through rather than
// hidden: a row that silently loses is an operator editing a form to no
// effect and finding out from a user.
//
// The client secret is never rendered. Not in full, not masked, not by
// length. The page shows only that one is held and when it last
// changed, because "when did this change" is the question an operator
// actually has.

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type AdminConnector,
  type AdminConnectorCatalogEntry,
  type AdminConnectorDiscovery,
  type AdminConnectorRequest,
  type AdminConnectorTest,
} from "@/lib/api";
import { ChipList } from "@/components/ChipList";
import { ConfirmButton } from "@/components/ConfirmButton";
import { GroupsField } from "@/components/GroupsField";
import { connectorLabel } from "@/lib/connectorNames";
import { EVERYONE, parseGroupList } from "@/lib/siteapps";

const TH = "px-4 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-500";
const TD = "px-4 py-2 align-top text-sm text-gray-800";

function Code({ children }: { children: React.ReactNode }) {
  return (
    <code className="rounded bg-gray-100 px-1 py-0.5 font-mono text-xs text-gray-800">
      {children}
    </code>
  );
}

function when(value?: string) {
  if (!value) return "never";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString();
}

export default function ConnectorsPage() {
  const qc = useQueryClient();
  const [tests, setTests] = useState<Record<string, AdminConnectorTest>>({});
  const [editing, setEditing] = useState<string | null>(null);

  const { data, isLoading, error } = useQuery({
    queryKey: ["admin", "connectors"],
    queryFn: api.admin.listConnectors,
  });

  // The shipped catalogue, so the form can offer services rather than ask
  // for an id. A failure here is not fatal to the page: the list above is
  // what an operator came to read, and a site connector needs no
  // catalogue at all.
  const catalog = useQuery({
    queryKey: ["admin", "connectors", "catalog"],
    queryFn: api.admin.connectorCatalog,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["admin", "connectors"] });

  const save = useMutation({
    mutationFn: (req: AdminConnectorRequest) => api.admin.saveConnector(req),
    onSuccess: () => {
      setEditing(null);
      void invalidate();
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.admin.deleteConnector(id),
    onSuccess: () => void invalidate(),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.admin.revokeConnector(id),
    onSuccess: () => void invalidate(),
  });
  const test = useMutation({
    mutationFn: (id: string) => api.admin.testConnector(id),
    onSuccess: (result) => setTests((t) => ({ ...t, [result.id]: result })),
  });

  if (isLoading) return <p className="p-6 text-sm text-gray-600">Loading connectors…</p>;
  if (error) {
    return (
      <p className="p-6 text-sm text-red-700" data-testid="connectors-error">
        Could not load connectors: {String(error)}
      </p>
    );
  }

  const rows = data ?? [];

  return (
    <div className="space-y-6 p-6">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold text-gray-900">Connectors</h1>
        <p className="max-w-3xl text-sm text-gray-600">
          Accounts this access point connects <strong>to</strong> on a user&apos;s behalf, so a job
          can use a credential they granted — Google Drive, for instance. This is the opposite
          direction from <strong>OAuth2 Clients</strong>, which are applications connecting{" "}
          <strong>to</strong> this server.
        </p>
      </header>

      {rows.length === 0 ? (
        <p className="text-sm text-gray-600" data-testid="connectors-empty">
          No connectors are configured. Add one below, or set{" "}
          <Code>HTTP_API_CONNECTORS</Code> and the matching{" "}
          <Code>HTTP_API_CONNECTOR_&lt;NS&gt;_&lt;NAME&gt;_*</Code> parameters — the difference
          is whether changing it later needs a <Code>condor_reconfig</Code>.
        </p>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-gray-200 bg-white">
          <table className="min-w-full" data-testid="connectors-table">
            <thead className="bg-gray-50">
              <tr>
                <th className={TH}>Connector</th>
                <th className={TH}>Source</th>
                <th className={TH}>Groups</th>
                <th className={TH}>Secret</th>
                <th className={TH}>Use</th>
                <th className={TH}>Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {rows.map((c) => (
                <ConnectorRow
                  key={c.id}
                  c={c}
                  test={tests[c.id]}
                  busy={save.isPending || remove.isPending || revoke.isPending}
                  onTest={() => test.mutate(c.id)}
                  onRevoke={() => revoke.mutate(c.id)}
                  onDelete={() => remove.mutate(c.id)}
                  onEdit={() => setEditing(c.id)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      <ConnectorForm
        /* Remounted when the subject changes, which is what resets the
           fields. The alternative -- an effect that writes every field on
           a change of editingID -- is a cascade of setState calls inside
           an effect, and it was also the version that left Edit opening a
           blank form and saving the blanks. */
        key={editing ?? "new"}
        catalog={catalog.data?.builtins ?? []}
        editingID={editing}
        existing={rows.find((r) => r.id === editing)}
        onCancel={() => setEditing(null)}
        onSave={(req) => save.mutate(req)}
        pending={save.isPending}
        error={save.error ? String(save.error) : null}
      />
    </div>
  );
}

function ConnectorRow({
  c,
  test,
  busy,
  onTest,
  onRevoke,
  onDelete,
  onEdit,
}: {
  c: AdminConnector;
  test?: AdminConnectorTest;
  busy: boolean;
  onTest: () => void;
  onRevoke: () => void;
  onDelete: () => void;
  onEdit: () => void;
}) {
  return (
    <tr data-testid={`connector-${c.id}`} className={c.shadowed ? "bg-amber-50" : undefined}>
      <td className={TD}>
        {/* The service first, the id under it. The id is how the server
            identifies a connector and not what anybody calls it, and an
            operator scanning this table is looking for the service. */}
        <div className="text-sm text-gray-900">{connectorLabel(c.id)}</div>
        <div className="font-mono text-xs text-gray-500">{c.id}</div>
        {/* Transfer lives here rather than in a column of its own. It is
            "spool" for nearly every row, and a column that reads the same
            all the way down is a column nobody reads. */}
        <div className="text-xs text-gray-500">
          {c.kind} · {c.transfer}
          {c.transfer === "direct" && !c.transfer_method && (
            <span className="text-amber-700"> — no transfer plugin ships for this</span>
          )}
        </div>
        {!c.enabled && <div className="text-xs font-semibold text-amber-700">disabled</div>}
        {test && (
          <div
            className={`mt-1 text-xs ${test.usable ? "text-green-700" : "text-red-700"}`}
            data-testid={`connector-test-${c.id}`}
          >
            {test.usable ? "Usable." : test.problems.join("; ")}
          </div>
        )}
      </td>

      <td className={TD}>
        {c.stored_here ? (
          <>
            <span className="text-sm">Stored here</span>
            {c.shadowed && (
              <p className="mt-1 text-xs text-amber-800" data-testid={`connector-shadowed-${c.id}`}>
                The configuration also declares this one and takes precedence, so these stored
                values are <strong>not</strong> in force. Remove it from the configuration, or
                delete this row.
              </p>
            )}
          </>
        ) : (
          <>
            <span className="text-sm">Configuration</span>
            <p className="mt-1 text-xs text-gray-600">
              Change it there and run <Code>condor_reconfig</Code>.
            </p>
          </>
        )}
      </td>

      <td className={TD}>
        {c.groups.length === 0 ? (
          <span className="text-xs text-amber-700" data-testid={`connector-nogroups-${c.id}`}>
            none — nobody may use it
          </span>
        ) : (
          <ChipList items={c.groups} />
        )}
      </td>

      <td className={TD} data-testid={`connector-secret-${c.id}`}>
        {c.secret_set ? (
          <>
            <span className="text-sm text-green-700">set</span>
            <div className="text-xs text-gray-500">changed {when(c.secret_updated_at)}</div>
          </>
        ) : (
          <span className="text-sm text-red-700">not set</span>
        )}
        {c.secret_file && (
          <div className="mt-1 break-all font-mono text-xs text-gray-500">{c.secret_file}</div>
        )}
      </td>

      <td className={TD}>
        <div className="text-sm">
          {c.users} {c.users === 1 ? "user" : "users"}
        </div>
        <div className="text-xs text-gray-500">last used {when(c.last_used_at)}</div>
      </td>

      <td className={TD}>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            onClick={onTest}
            disabled={busy}
            className="rounded border border-gray-300 px-2 py-1 text-xs hover:bg-gray-50 disabled:opacity-50"
          >
            Test
          </button>

          {/* Revoke stays available on a configuration-declared
              connector. The configuration owns the definition; these
              credentials are this server's own state, and an operator
              who cannot revoke them here has to delete database rows by
              hand. */}
          <ConfirmButton
            onConfirm={onRevoke}
            disabled={busy || c.users === 0}
            label="Revoke"
            confirmLabel={`Revoke ${c.users} credential${c.users === 1 ? "" : "s"}?`}
            title="Users will have to connect again. Use this when a client secret has leaked or rotated."
          />

          {c.editable && (
            <button
              type="button"
              onClick={onEdit}
              disabled={busy}
              className="rounded border border-gray-300 px-2 py-1 text-xs hover:bg-gray-50 disabled:opacity-50"
            >
              Edit
            </button>
          )}
          {c.editable && (
            <ConfirmButton
              onConfirm={onDelete}
              disabled={busy}
              label="Delete"
              confirmLabel={
                c.users > 0
                  ? `Delete, destroying ${c.users} credential${c.users === 1 ? "" : "s"}?`
                  : "Delete?"
              }
              title="Removes the connector and every credential users granted it."
            />
          )}
        </div>
      </td>
    </tr>
  );
}

// The form. The id decides its shape, and the id is now CHOSEN rather than
// typed: a radio picks the namespace and a select picks the provider, so
// nobody assembles `builtin:` + a name in a text box that can only be
// checked after the save comes back.
//
// A built-in needs only what is the operator's -- the client registration
// and who may use it -- because the shipped definition carries the
// endpoints and scopes. A site connector needs those too, and asks for an
// ISSUER first: a provider publishes its endpoints in a metadata document,
// and transcribing two URLs out of its documentation instead is a typo that
// surfaces at consent time, in a user's browser. Typing them directly is
// still there, under a disclosure, for a provider that publishes nothing.
//
// Two controls are absent unless the chosen provider has them. Direct
// transfer needs a file transfer plugin on the execute node, and for a
// provider with none the control is a choice with one answer; a file
// chooser is a Google Drive fact, and mentioning it anywhere else is an
// operator looking for a setting that does not exist.
function ConnectorForm({
  editingID,
  existing,
  catalog,
  onCancel,
  onSave,
  pending,
  error,
}: {
  editingID: string | null;
  existing?: AdminConnector;
  catalog: AdminConnectorCatalogEntry[];
  onCancel: () => void;
  onSave: (req: AdminConnectorRequest) => void;
  pending: boolean;
  error: string | null;
}) {
  // Seeded from the row being edited. Clicking Edit used to open a form
  // with every field blank and a Save that wrote the blanks -- which
  // emptied the groups, and empty groups is a connector nobody may use.
  //
  // The secret is deliberately not seeded: it is never sent to this page,
  // and a blank field here means "leave the stored one alone".
  const [namespace, setNamespace] = useState<"builtin" | "site">(
    editingID?.startsWith("site:") ? "site" : "builtin",
  );
  const [builtinID, setBuiltinID] = useState(editingID?.startsWith("builtin:") ? editingID : "");
  const [siteName, setSiteName] = useState(
    editingID?.startsWith("site:") ? editingID.slice("site:".length) : "",
  );
  const [clientID, setClientID] = useState(existing?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const [issuer, setIssuer] = useState("");
  const [authURL, setAuthURL] = useState("");
  const [tokenURL, setTokenURL] = useState("");
  const [manualEndpoints, setManualEndpoints] = useState(false);
  const [everyone, setEveryone] = useState(existing?.groups.includes(EVERYONE) ?? false);
  const [groups, setGroups] = useState<string[]>(
    (existing?.groups ?? []).filter((g) => g !== EVERYONE),
  );
  const [pendingGroup, setPendingGroup] = useState("");
  const [transfer, setTransfer] = useState(existing?.transfer ?? "spool");
  const [discovered, setDiscovered] = useState<AdminConnectorDiscovery | null>(null);

  const effectiveID =
    editingID ?? (namespace === "builtin" ? builtinID : siteName ? `site:${siteName}` : "");
  const isBuiltin = effectiveID.startsWith("builtin:");
  const chosen = catalog.find((c) => c.id === effectiveID);

  // Only a built-in can offer direct transfer, and only one with a plugin.
  // For everything else spool is the single working answer, so the form
  // states it rather than asking.
  const canChooseTransfer = Boolean(chosen?.transfer_method);

  const discover = useMutation({
    mutationFn: (value: string) => api.admin.discoverIssuer(value),
    onSuccess: (result) => {
      setDiscovered(result);
      setAuthURL(result.authorization_url ?? "");
      setTokenURL(result.token_url ?? "");
      // A document missing the token endpoint leaves something to fill in
      // by hand, so open the advanced panel rather than hiding the gap.
      if (!result.token_url) setManualEndpoints(true);
    },
  });

  const effectiveGroups = everyone
    ? [EVERYONE]
    : // Whatever is still in the group box counts: nobody should have to
      // press Enter for a name they can see in the field.
      pendingGroup.trim() === ""
      ? groups
      : [...groups, ...parseGroupList(pendingGroup)];

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const req: AdminConnectorRequest = {
      id: effectiveID,
      kind: isBuiltin ? undefined : "oauth2",
      client_id: clientID || existing?.client_id,
      authorization_url: authURL || undefined,
      token_url: tokenURL || undefined,
      groups: effectiveGroups,
      transfer: canChooseTransfer ? transfer : "spool",
    };
    // Only send the secret when one was typed. An empty string would CLEAR
    // a stored secret, so an edit that does not mention it must not carry
    // one.
    if (secret !== "") req.client_secret = secret;
    onSave(req);
  };

  return (
    <form
      onSubmit={submit}
      className="max-w-2xl space-y-4 rounded-lg border border-gray-200 bg-white p-4"
      data-testid="connector-form"
    >
      <h2 className="text-sm font-semibold text-gray-900">
        {editingID ? `Edit ${editingID}` : "Add a connector"}
      </h2>

      {!editingID && (
        <fieldset className="space-y-2" data-testid="connector-namespace">
          <legend className="text-sm text-gray-700">Provider</legend>
          {(
            [
              {
                value: "builtin" as const,
                label: "Built-in",
                hint: "Ships with this server, with its endpoints and scopes already set.",
              },
              {
                value: "site" as const,
                label: "Site-defined",
                hint: "Any other OAuth2 provider. You supply its endpoints.",
              },
            ]
          ).map((opt) => (
            <label key={opt.value} className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="connector-namespace"
                className="mt-1"
                value={opt.value}
                checked={namespace === opt.value}
                onChange={() => setNamespace(opt.value)}
                data-testid={`connector-namespace-${opt.value}`}
              />
              <span>
                <span className="text-gray-900">{opt.label}</span>
                <span className="block text-xs text-gray-500">{opt.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>
      )}

      {!editingID && namespace === "builtin" && (
        <label className="block text-sm">
          <span className="text-gray-700">Service</span>
          <select
            value={builtinID}
            onChange={(e) => setBuiltinID(e.target.value)}
            className="mt-1 w-full rounded border border-gray-300 px-2 py-1 text-sm"
            data-testid="connector-builtin"
          >
            <option value="">Choose a service…</option>
            {catalog.map((c) => (
              // An already-configured one stays listed and is not
              // selectable: offering it would produce a save the server
              // refuses, and leaving it out makes an operator wonder
              // whether this server supports it at all.
              <option key={c.id} value={c.id} disabled={c.configured && c.id !== editingID}>
                {connectorLabel(c.id)}
                {c.configured ? " — already configured" : ""}
              </option>
            ))}
          </select>
        </label>
      )}

      {!editingID && namespace === "site" && (
        <label className="block text-sm">
          <span className="text-gray-700">Name</span>
          <span className="mt-1 flex items-stretch">
            {/* The namespace is shown, not typed. It is part of the id the
                server stores and an operator should see it, but typing it
                is how `site;cryoem` and `Site:cryoem` happen. */}
            <span className="inline-flex items-center rounded-l border border-r-0 border-gray-300 bg-gray-50 px-2 font-mono text-sm text-gray-500">
              site:
            </span>
            <input
              value={siteName}
              onChange={(e) => setSiteName(e.target.value)}
              placeholder="cryoem"
              className="w-full rounded-r border border-gray-300 px-2 py-1 font-mono text-sm"
              data-testid="connector-site-name"
            />
          </span>
          <span className="text-xs text-gray-500">
            Lowercase letters, digits and hyphen. This is what users will see in their
            connected-services list.
          </span>
        </label>
      )}

      {chosen && (
        <div
          className="rounded border border-gray-200 bg-gray-50 p-3 text-xs text-gray-600"
          data-testid="connector-builtin-detail"
        >
          <p className="text-gray-800">{chosen.summary}</p>
          {chosen.default_scopes && chosen.default_scopes.length > 0 && (
            <p className="mt-1">
              Asks each user for <Code>{chosen.default_scopes.join(" ")}</Code>.
            </p>
          )}
          {/* Only Drive has one today, which is the point: a chooser is a
              provider's, and mentioning it under a provider that has none
              sends an operator looking for a setting that does not exist. */}
          {chosen.has_picker && (
            <p className="mt-1" data-testid="connector-picker-note">
              A browser file chooser ships for this provider, so users can pick files rather
              than paste ids. It is configured separately, with <Code>HTTP_API_PICKERS</Code>
              {chosen.picker_needs_api_key
                ? ", and needs a developer key from the provider — the chooser loads without one and then refuses."
                : "."}
            </p>
          )}
          {!chosen.transfer_method && (
            <p className="mt-1">
              No file transfer plugin ships for this provider, so this server fetches the
              files itself and nothing is deployed to execute nodes.
            </p>
          )}
        </div>
      )}

      {!isBuiltin && effectiveID !== "" && (
        <div className="space-y-2 rounded border border-gray-200 p-3">
          <label className="block text-sm">
            <span className="text-gray-700">Issuer</span>
            <span className="mt-1 flex gap-2">
              <input
                value={issuer}
                onChange={(e) => setIssuer(e.target.value)}
                placeholder="https://login.example.edu"
                className="w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
                data-testid="connector-issuer"
              />
              <button
                type="button"
                onClick={() => discover.mutate(issuer)}
                disabled={issuer.trim() === "" || discover.isPending}
                className="shrink-0 rounded border border-gray-300 px-2 py-1 text-xs hover:bg-gray-50 disabled:opacity-50"
                data-testid="connector-discover"
              >
                {discover.isPending ? "Asking…" : "Discover"}
              </button>
            </span>
            <span className="text-xs text-gray-500">
              Most providers publish their endpoints. This server asks for them, so you do not
              have to transcribe two URLs that fail at sign-in if either is wrong.
            </span>
          </label>

          {discover.error && (
            <p className="text-xs text-red-700" data-testid="connector-discover-error">
              {String(discover.error)}
            </p>
          )}

          {discovered && (
            <div className="text-xs text-gray-600" data-testid="connector-discovered">
              <p className="text-green-700">
                Read from <span className="break-all font-mono">{discovered.document}</span>
              </p>
              {discovered.scopes_supported && discovered.scopes_supported.length > 0 && (
                <p className="mt-1">
                  Scopes it offers: {discovered.scopes_supported.slice(0, 12).join(", ")}
                  {discovered.scopes_supported.length > 12 ? "…" : ""}
                </p>
              )}
              {(discovered.warnings ?? []).map((wmsg) => (
                <p key={wmsg} className="mt-1 text-amber-800" data-testid="connector-discover-warning">
                  {wmsg}
                </p>
              ))}
            </div>
          )}

          <button
            type="button"
            onClick={() => setManualEndpoints((v) => !v)}
            className="text-xs text-gray-600 underline"
            aria-expanded={manualEndpoints}
            data-testid="connector-manual-toggle"
          >
            {manualEndpoints ? "Hide the endpoints" : "Set the endpoints myself"}
          </button>

          {manualEndpoints && (
            <>
              <label className="block text-sm">
                <span className="text-gray-700">Authorization URL</span>
                <input
                  value={authURL}
                  onChange={(e) => setAuthURL(e.target.value)}
                  className="mt-1 w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
                  data-testid="connector-auth-url"
                />
              </label>
              <label className="block text-sm">
                <span className="text-gray-700">Token URL</span>
                <input
                  value={tokenURL}
                  onChange={(e) => setTokenURL(e.target.value)}
                  className="mt-1 w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
                  data-testid="connector-token-url"
                />
              </label>
            </>
          )}
        </div>
      )}

      <label className="block text-sm">
        <span className="text-gray-700">Client id</span>
        <input
          value={clientID}
          onChange={(e) => setClientID(e.target.value)}
          className="mt-1 w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
          data-testid="connector-client-id"
        />
        <span className="text-xs text-gray-500">
          From the application you registered with the provider.
        </span>
      </label>

      <label className="block text-sm">
        <span className="text-gray-700">Client secret</span>
        <input
          type="password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          autoComplete="new-password"
          className="mt-1 w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
          data-testid="connector-secret"
        />
        <span className="text-xs text-gray-500">
          {existing?.secret_set
            ? "A secret is stored. Leave this blank to keep it."
            : "Stored encrypted. It is never shown again."}
        </span>
      </label>

      <GroupsField
        label="Who may connect an account"
        everyoneLabel="Everyone signed in to this access point"
        testIDPrefix="connector"
        everyone={everyone}
        groups={groups}
        pending={pendingGroup}
        error={
          effectiveGroups.length === 0
            ? "Name at least one group, or tick “everyone signed in”. With none, nobody may use this connector."
            : null
        }
        onEveryone={setEveryone}
        onGroups={setGroups}
        onPending={setPendingGroup}
      />

      {canChooseTransfer && (
        <label className="block text-sm">
          <span className="text-gray-700">Transfer</span>
          <select
            value={transfer}
            onChange={(e) => setTransfer(e.target.value)}
            className="mt-1 rounded border border-gray-300 px-2 py-1 text-sm"
            data-testid="connector-transfer"
          >
            <option value="spool">spool — this server fetches the files</option>
            <option value="direct">
              direct — the execute node fetches them, with the {chosen?.transfer_method} plugin
            </option>
          </select>
          <span className="block text-xs text-gray-500">
            <Code>spool</Code> keeps third-party tokens off execute nodes and needs nothing
            deployed there.
          </span>
        </label>
      )}

      {error && (
        <p className="text-sm text-red-700" data-testid="connector-form-error">
          {error}
        </p>
      )}

      <div className="flex gap-2">
        <button
          type="submit"
          disabled={pending || effectiveID === "" || effectiveGroups.length === 0}
          className="rounded bg-gray-900 px-3 py-1 text-sm text-white disabled:opacity-50"
          data-testid="connector-save"
        >
          {pending ? "Saving…" : "Save"}
        </button>
        {editingID && (
          <button
            type="button"
            onClick={onCancel}
            className="rounded border border-gray-300 px-3 py-1 text-sm"
          >
            Cancel
          </button>
        )}
      </div>
    </form>
  );
}
