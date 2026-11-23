"use client";

// OAuth2 scopes, as chips, with the ones that reach beyond their holder
// marked.
//
// Lifted out of the admin token page so the user-facing connectors page
// shows the same thing the same way. They had drifted: the admin page used
// solid blue with amber for a cross-user power, the connectors page used
// ringed monospace chips with no marking at all -- so an app holding
// anyuser:read was flagged to an operator and not to the person whose
// account it was reading.
//
// Toggling is the admin half and stays optional. Narrowing a grant is an
// operator's act; a user's answer to a scope they dislike is to
// disconnect, which is a different button.

/**
 * Scopes that act on other people's jobs rather than the holder's own.
 *
 * Both spellings of both powers. A grant minted before the rename carries
 * the older one, and a cross-user power left unmarked here is exactly the
 * one somebody is scanning for.
 */
export const privilegedScopes = new Set([
  "anyuser:read",
  "anyuser:write",
  "mcp:admin",
  "mcp:superuser",
]);

export function ScopeChips({
  scopes,
  authorizedScopes,
  onToggle,
  busy = false,
  empty = "—",
}: {
  /** The scopes in force. */
  scopes: string[] | undefined | null;
  /** Every scope the authorization ended with, whether or not it is in
   *  force. Showing only those in force is what made switching a scope off
   *  look like deleting it, with no way back. Omit where there is no such
   *  distinction to draw. */
  authorizedScopes?: string[] | null;
  /** Switches a scope off or back on. Omitted renders plain chips. */
  onToggle?: (scope: string, enable: boolean) => void;
  busy?: boolean;
  empty?: string;
}) {
  const active = new Set(scopes ?? []);
  const all = authorizedScopes?.length ? authorizedScopes : (scopes ?? []);
  if (all.length === 0) {
    return <span className="text-gray-400">{empty}</span>;
  }

  return (
    <div className="flex flex-wrap gap-1">
      {all.map((scope) => {
        const on = active.has(scope);
        const privileged = privilegedScopes.has(scope);
        const tone = !on
          ? "bg-gray-100 text-gray-400 line-through ring-1 ring-gray-300"
          : privileged
            ? "bg-amber-100 text-amber-900 ring-1 ring-amber-300"
            : "bg-blue-100 text-blue-800";
        const body = (
          <>
            {scope}
            {privileged && on && (
              <span className="text-[10px] font-semibold uppercase tracking-wide">
                other users
              </span>
            )}
          </>
        );
        const shape = `inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs ${tone}`;

        if (!onToggle) {
          return (
            <span key={scope} className={shape} data-testid={`scope-${scope}`}>
              {body}
            </span>
          );
        }
        const label = on
          ? `Switch ${scope} off for this grant and its paired token`
          : `Switch ${scope} back on`;
        return (
          <button
            key={scope}
            type="button"
            onClick={() => onToggle(scope, !on)}
            disabled={busy}
            aria-pressed={on}
            // The visible text is the scope, so without this the button
            // announces itself as "mcp:write" and says nothing about what
            // clicking it does -- to a screen reader or to a test.
            aria-label={label}
            title={label}
            className={`${shape} cursor-pointer hover:brightness-95 disabled:opacity-60`}
            data-testid={`scope-${scope}`}
          >
            {body}
          </button>
        );
      })}
    </div>
  );
}
