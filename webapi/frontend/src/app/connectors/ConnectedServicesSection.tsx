"use client";

// Services you connected: accounts at other services that this access point
// may read or write on your behalf.
//
// The third direction on this page, and the one where the credential being
// held is OURS. An app in the first section holds a credential to act as you
// here; a webhook in the second is us posting out and holding nothing. Here
// we hold a token on your account somewhere else, which is why this is the
// section with a Disconnect button rather than a Delete one: the grant is
// yours to withdraw, and withdrawing it at the provider is a separate act we
// cannot do for you.
//
// Connecting is a browser NAVIGATION rather than a fetch. The flow ends at
// the provider's own consent screen and comes back through a callback on this
// server, so there is nothing for an XHR to return; connect_url is a fixed,
// printable entry point that carries no authority of its own.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type ConnectorStatus } from "@/lib/api";
import { connectorLabel } from "@/lib/connectorNames";
import { ConfirmButton } from "@/components/ConfirmButton";

const TH = "px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-500";

export function ConnectedServicesSection() {
  const qc = useQueryClient();
  const { data, isLoading, error } = useQuery({
    queryKey: ["connect"],
    queryFn: api.connect.list,
    retry: false,
  });

  const disconnect = useMutation({
    mutationFn: (id: string) => api.connect.disconnect(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["connect"] }),
  });

  // Multi-AP mode does not serve these endpoints, and a site may configure no
  // connectors at all. Neither is an error worth showing: the section simply
  // is not part of this deployment, and a red box would read as something
  // broken. The two cases above it are this page's own and stay visible.
  if (error) return null;
  if (!isLoading && (data ?? []).length === 0) return null;

  const connectors = data ?? [];

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold text-gray-900">
          Services you connected
        </h2>
        <p className="mt-1 text-sm text-gray-500">
          Accounts this access point may use on your behalf — so a job can read
          its input straight from one, without you downloading and re-uploading
          it. Nothing here can see your jobs.
        </p>
      </div>

      {isLoading && <p className="text-sm text-gray-500">Loading…</p>}

      <div className="overflow-x-auto rounded-lg border border-gray-200">
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50">
            <tr>
              <th className={TH}>Service</th>
              <th className={TH}>Status</th>
              <th className={TH}>Last used</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 bg-white">
            {connectors.map((c) => (
              <ConnectorRow
                key={c.id}
                connector={c}
                onDisconnect={() => disconnect.mutate(c.id)}
                disconnecting={
                  disconnect.isPending && disconnect.variables === c.id
                }
              />
            ))}
          </tbody>
        </table>
      </div>

      {disconnect.error != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(disconnect.error as Error).message}
        </p>
      )}
    </section>
  );
}

function ConnectorRow({
  connector,
  onDisconnect,
  disconnecting,
}: {
  connector: ConnectorStatus;
  onDisconnect: () => void;
  disconnecting: boolean;
}) {
  const label = connectorLabel(connector.id);
  return (
    <tr>
      <td className="px-3 py-2">
        <div className="text-gray-900">{label}</div>
        {/* The id, because a site may configure two connectors to one
            provider and the friendly name alone would not tell them apart. */}
        <div className="font-mono text-xs text-gray-500">{connector.id}</div>
      </td>
      <td className="px-3 py-2">
        {/* needs_consent is a grant the provider will no longer renew, which
            is a different thing from never having connected and needs a
            different sentence: reconnecting fixes it, and nothing else will. */}
        {connector.needs_consent ? (
          <span className="inline-flex rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-900">
            Needs reconnecting
          </span>
        ) : connector.connected ? (
          <span className="inline-flex rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
            Connected
          </span>
        ) : (
          <span className="inline-flex rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
            Not connected
          </span>
        )}
        {connector.needs_consent && (
          <div className="mt-1 text-xs text-gray-500">
            The provider stopped renewing this. Reconnecting fixes it; nothing else will.
          </div>
        )}
      </td>
      <td className="px-3 py-2 text-gray-700">
        {connector.last_used_at
          ? new Date(connector.last_used_at).toLocaleString()
          : "—"}
      </td>
      <td className="px-3 py-2 text-right">
        {connector.connected || connector.needs_consent ? (
          <span className="inline-flex items-center gap-2">
            {connector.needs_consent && (
              <a
                href={connector.connect_url}
                className="rounded-sm border border-amber-400 bg-white px-3 py-1 text-xs font-medium text-amber-900 hover:bg-amber-100"
              >
                Reconnect
              </a>
            )}
            <ConfirmButton
              label="Disconnect"
              title="Forget this account's credential on this access point"
              compact
              pending={disconnecting}
              onConfirm={onDisconnect}
            />
          </span>
        ) : (
          /* A link, not a button: the flow is a navigation that ends at the
             provider's consent screen. */
          <a
            href={connector.connect_url}
            className="rounded-sm bg-blue-600 px-3 py-1 text-xs font-medium text-white hover:bg-blue-700"
          >
            Connect
          </a>
        )}
      </td>
    </tr>
  );
}
