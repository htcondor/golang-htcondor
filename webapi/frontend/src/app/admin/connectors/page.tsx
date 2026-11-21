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
  type AdminConnectorRequest,
  type AdminConnectorTest,
} from "@/lib/api";
import { ChipList } from "@/components/ChipList";
import { ConfirmButton } from "@/components/ConfirmButton";

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
          No connectors are configured. Set <Code>HTTP_API_CONNECTORS</Code> and the matching{" "}
          <Code>HTTP_API_CONNECTOR_&lt;NS&gt;_&lt;NAME&gt;_*</Code> parameters, or add one below.
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
                <th className={TH}>Transfer</th>
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
        <div className="font-mono text-sm">{c.id}</div>
        <div className="text-xs text-gray-500">{c.kind}</div>
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
        <div className="text-sm">{c.transfer}</div>
        <div className="text-xs text-gray-500" data-testid={`connector-picker-${c.id}`}>
          chooser {c.picker ? "on" : "off"}
        </div>
        {c.transfer === "direct" && !c.transfer_method && (
          <div className="text-xs text-amber-700">no transfer plugin ships for this</div>
        )}
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

// The form. Two shapes, decided by the namespace: a builtin needs only
// what is the operator's (client registration and groups), because the
// shipped definition carries the endpoints and scopes. A site connector
// needs all of it.
function ConnectorForm({
  editingID,
  existing,
  onCancel,
  onSave,
  pending,
  error,
}: {
  editingID: string | null;
  existing?: AdminConnector;
  onCancel: () => void;
  onSave: (req: AdminConnectorRequest) => void;
  pending: boolean;
  error: string | null;
}) {
  const [id, setID] = useState("");
  const [clientID, setClientID] = useState("");
  const [secret, setSecret] = useState("");
  const [authURL, setAuthURL] = useState("");
  const [tokenURL, setTokenURL] = useState("");
  const [groups, setGroups] = useState("");
  const [transfer, setTransfer] = useState("spool");
  const [picker, setPicker] = useState(false);

  const effectiveID = editingID ?? id;
  const isBuiltin = effectiveID.startsWith("builtin:");

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const req: AdminConnectorRequest = {
      id: effectiveID,
      kind: isBuiltin ? undefined : "oauth2",
      client_id: clientID || existing?.client_id,
      authorization_url: authURL || undefined,
      token_url: tokenURL || undefined,
      groups: groups
        .split(",")
        .map((g) => g.trim())
        .filter(Boolean),
      transfer,
      picker,
    };
    // Only send the secret when one was typed. An empty string would
    // CLEAR a stored secret, so an edit that does not mention it must
    // not carry one.
    if (secret !== "") req.client_secret = secret;
    onSave(req);
  };

  return (
    <form
      onSubmit={submit}
      className="max-w-2xl space-y-3 rounded-lg border border-gray-200 bg-white p-4"
      data-testid="connector-form"
    >
      <h2 className="text-sm font-semibold text-gray-900">
        {editingID ? `Edit ${editingID}` : "Add a connector"}
      </h2>

      {!editingID && (
        <label className="block text-sm">
          <span className="text-gray-700">Id</span>
          <input
            value={id}
            onChange={(e) => setID(e.target.value)}
            placeholder="builtin:gdrive or site:cryoem"
            className="mt-1 w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
            data-testid="connector-id"
          />
          <span className="text-xs text-gray-500">
            <Code>builtin:</Code> definitions ship with this server and supply their own endpoints
            and scopes. <Code>site:</Code> is yours and supplies everything. Names use lowercase
            letters, digits and hyphen.
          </span>
        </label>
      )}

      <label className="block text-sm">
        <span className="text-gray-700">Client id</span>
        <input
          value={clientID}
          onChange={(e) => setClientID(e.target.value)}
          className="mt-1 w-full rounded border border-gray-300 px-2 py-1 font-mono text-sm"
          data-testid="connector-client-id"
        />
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

      {!isBuiltin && (
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

      <label className="block text-sm">
        <span className="text-gray-700">Groups</span>
        <input
          value={groups}
          onChange={(e) => setGroups(e.target.value)}
          placeholder="smith_lab, jones_lab"
          className="mt-1 w-full rounded border border-gray-300 px-2 py-1 text-sm"
          data-testid="connector-groups"
        />
        <span className="text-xs text-gray-500">
          Who may connect an account. Empty means nobody; <Code>*</Code> means every signed-in user.
        </span>
      </label>

      <label className="block text-sm">
        <span className="text-gray-700">Transfer</span>
        <select
          value={transfer}
          onChange={(e) => setTransfer(e.target.value)}
          className="mt-1 rounded border border-gray-300 px-2 py-1 text-sm"
          data-testid="connector-transfer"
        >
          <option value="spool">spool — this server fetches the files</option>
          <option value="direct">direct — the execute node fetches them</option>
        </select>
        <span className="block text-xs text-gray-500">
          <Code>spool</Code> keeps third-party tokens off execute nodes and needs nothing deployed
          there.
        </span>
      </label>

      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={picker}
          onChange={(e) => setPicker(e.target.checked)}
          className="mt-1"
          data-testid="connector-picker"
        />
        <span>
          <span className="text-gray-700">Offer a file chooser</span>
          <span className="block text-xs text-gray-500">
            Lets people pick files in the submit page. This is the only setting that widens this
            site&apos;s Content-Security-Policy, because the chooser loads from the provider and
            renders in its iframe — so leave it off unless it is wanted.
          </span>
        </span>
      </label>

      {error && (
        <p className="text-sm text-red-700" data-testid="connector-form-error">
          {error}
        </p>
      )}

      <div className="flex gap-2">
        <button
          type="submit"
          disabled={pending || effectiveID === ""}
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
