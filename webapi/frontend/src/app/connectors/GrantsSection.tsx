"use client";

// Apps holding access to this account right now.
//
// The page already listed the clients you REGISTERED. It did not list what
// is actually holding a token, and those are not the same set: an
// assistant or an automation that set itself up through dynamic client
// registration appears in neither, so the only listing of it was
// /admin/tokens, behind the admin role. A person could connect something
// to their own account and then have no way to see it, let alone cut it
// off, without asking an operator.
//
// Direction, because this page turns on it. This belongs under "Apps
// connected to your account" and not under "Services you connected": what
// is listed here holds a credential to act as YOU HERE. "Services you
// connected" is the opposite arrow -- this server holding a credential on
// your account at Drive or Box -- and a row filed under the wrong arrow is
// how somebody revokes the wrong thing.
//
// Grouped by app rather than by token. One app that has signed in three
// times holds three grants and reads as one row, because "let this app in"
// is the decision a person made. Disconnecting takes all of them, and the
// refresh tokens with them: leaving one behind would be a Disconnect
// button that does not disconnect.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Grant } from "@/lib/api";
import { ConfirmButton } from "@/components/ConfirmButton";
import { ScopeChips } from "@/components/ScopeChips";

const TH = "px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-500";

export function GrantsSection() {
  const qc = useQueryClient();
  const { data, isLoading, error } = useQuery({
    queryKey: ["grants"],
    queryFn: api.grants.list,
    retry: false,
  });

  const disconnect = useMutation({
    mutationFn: (clientID: string) => api.grants.revoke(clientID),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["grants"] });
      // A disconnected app's "last used" is now history, so the list of
      // registered clients above is stale too.
      void qc.invalidateQueries({ queryKey: ["integrations"] });
    },
  });

  const grants = data?.grants ?? [];

  // A deployment without OAuth2 does not serve this, and an account with
  // nothing connected has nothing to say. Neither is worth a red box: the
  // first is not part of this deployment and the second is the ordinary
  // state of a new account.
  if (error) return null;
  if (!isLoading && grants.length === 0) return null;

  return (
    <div className="space-y-2" data-testid="grants-section">
      <div>
        <h3 className="text-sm font-semibold text-gray-900">Holding access right now</h3>
        <p className="mt-1 text-sm text-gray-500">
          Apps that can act as you on this access point today, including ones that set
          themselves up without an entry below. Disconnecting withdraws the tokens an app is
          holding; it can ask again by signing in.
        </p>
      </div>

      {isLoading && <p className="text-sm text-gray-500">Loading…</p>}

      {grants.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-gray-200">
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50">
              <tr>
                <th className={TH}>App</th>
                <th className={TH}>Granted</th>
                <th className={TH}>Access</th>
                <th className={TH}>Last signed in</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 bg-white">
              {grants.map((g) => (
                <GrantRow
                  key={g.client_id}
                  grant={g}
                  onDisconnect={() => disconnect.mutate(g.client_id)}
                  disconnecting={disconnect.isPending && disconnect.variables === g.client_id}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {disconnect.error != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(disconnect.error as Error).message}
        </p>
      )}
    </div>
  );
}

function GrantRow({
  grant,
  onDisconnect,
  disconnecting,
}: {
  grant: Grant;
  onDisconnect: () => void;
  disconnecting: boolean;
}) {
  return (
    <tr data-testid={`grant-${grant.client_id}`}>
      <td className="px-3 py-2">
        <div className="text-gray-900">{grant.client_name || grant.client_id}</div>
        {/* Which of the two kinds this is. An app the person registered is
            theirs to delete outright in the table below; one that
            registered itself can only be shut out, and saying so here
            saves a hunt for a Delete button that is not there. */}
        <div className="text-xs text-gray-500">
          {grant.owned_by_caller ? "Registered by you, below" : "Registered itself"}
        </div>
        <div className="font-mono text-xs text-gray-400">{grant.client_id}</div>
      </td>
      <td className="px-3 py-2">
        {/* What the sign-in actually GRANTED, which is not what the client
            asked for: the server narrows a request to what the account
            could consent to. The table below shows the asking. */}
        <ScopeChips scopes={grant.scopes} />
      </td>
      <td className="px-3 py-2">
        <span className="inline-flex rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
          Connected
        </span>
        {/* A refresh token is the difference between an app that stops
            working on its own and one that does not, which is the fact
            that decides whether disconnecting is worth doing. */}
        <div className="mt-1 text-xs text-gray-500">
          {grant.has_refresh ? "Renews itself without you" : "Expires on its own"}
        </div>
        {grant.grants > 1 && (
          <div className="text-xs text-gray-500">{grant.grants} sign-ins</div>
        )}
      </td>
      <td className="px-3 py-2 text-xs text-gray-700">
        {new Date(grant.last_seen).toLocaleString()}
        <div className="text-gray-500">
          first on {new Date(grant.first_seen).toLocaleDateString()}
        </div>
      </td>
      <td className="px-3 py-2 text-right">
        <ConfirmButton
          label="Disconnect"
          confirmLabel="Disconnect"
          title={`Withdraw every token ${
            grant.client_name || grant.client_id
          } holds for you, refresh tokens included`}
          compact
          pending={disconnecting}
          onConfirm={onDisconnect}
        />
      </td>
    </tr>
  );
}
