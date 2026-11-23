import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Grant } from '@/lib/api';

// What is holding access to this account right now.
//
// The behaviours that matter: an app the person never registered still
// shows up -- that is the gap this closes -- and disconnecting names the
// app rather than a token, because the decision being undone was "let this
// app in".

const list = vi.fn();
const revoke = vi.fn();

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      grants: { list: () => list(), revoke: (id: string) => revoke(id) },
    },
  };
});

function grant(over: Partial<Grant> = {}): Grant {
  return {
    client_id: 'client_abc',
    client_name: 'Assistant',
    owned_by_caller: false,
    scopes: ['openid', 'mcp:read'],
    grants: 1,
    tokens: 2,
    has_refresh: true,
    first_seen: '2026-11-01T10:00:00Z',
    last_seen: '2026-11-20T10:00:00Z',
    ...over,
  };
}

async function renderSection() {
  const { GrantsSection } = await import('./GrantsSection');
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <GrantsSection />
    </QueryClientProvider>,
  );
}

describe('the grants section', () => {
  beforeEach(() => {
    list.mockReset();
    revoke.mockReset().mockResolvedValue({ client_id: 'client_abc', revoked: 2 });
  });

  // The whole reason this exists: an app that set itself up through
  // dynamic registration is in no other list the user can reach.
  it('lists an app the user never registered, and says so', async () => {
    list.mockResolvedValue({ grants: [grant()] });
    await renderSection();

    await waitFor(() => expect(screen.getByTestId('grant-client_abc')).toBeTruthy());
    const row = screen.getByTestId('grant-client_abc');
    expect(row.textContent).toContain('Assistant');
    expect(row.textContent).toContain('Registered itself');
    expect(row.textContent).toContain('client_abc');
  });

  // The other kind reads differently, because an app the person registered
  // is theirs to delete outright in the table below and this one can only
  // be shut out.
  it('marks an app the user registered here', async () => {
    list.mockResolvedValue({ grants: [grant({ owned_by_caller: true })] });
    await renderSection();
    await waitFor(() => expect(screen.getByTestId('grant-client_abc')).toBeTruthy());
    expect(screen.getByTestId('grant-client_abc').textContent).toContain('Registered by you');
  });

  // Granted scopes, rendered the way the admin token page renders them, so
  // a cross-user power is flagged to the person whose account it reads --
  // which it was not when this page used plain chips.
  it('flags a scope that reaches other users', async () => {
    list.mockResolvedValue({
      grants: [grant({ scopes: ['mcp:read', 'anyuser:read'] })],
    });
    await renderSection();
    await waitFor(() => expect(screen.getByTestId('scope-anyuser:read')).toBeTruthy());
    expect(screen.getByTestId('scope-anyuser:read').textContent).toContain('other users');
    // And an ordinary scope is not flagged, so this is not "everything
    // carries the warning".
    expect(screen.getByTestId('scope-mcp:read').textContent).not.toContain('other users');
  });

  // Disconnecting is per APP. The confirmation and the call both name the
  // client, not a token fingerprint.
  it('disconnects the app by client id', async () => {
    list.mockResolvedValue({ grants: [grant()] });
    await renderSection();
    await waitFor(() => expect(screen.getByTestId('grant-client_abc')).toBeTruthy());

    // ConfirmButton asks first; the first click is the ask.
    fireEvent.click(screen.getByText('Disconnect'));
    const confirm = await screen.findAllByText('Disconnect');
    fireEvent.click(confirm[confirm.length - 1]);

    await waitFor(() => expect(revoke).toHaveBeenCalledWith('client_abc'));
  });

  // Whether an app can carry on without the person present is the fact
  // that decides whether disconnecting is worth doing.
  it('says whether the app can renew itself', async () => {
    list.mockResolvedValue({
      grants: [
        grant({ client_id: 'renews', has_refresh: true }),
        grant({ client_id: 'expires', has_refresh: false }),
      ],
    });
    await renderSection();
    await waitFor(() => expect(screen.getByTestId('grant-renews')).toBeTruthy());
    expect(screen.getByTestId('grant-renews').textContent).toContain('Renews itself');
    expect(screen.getByTestId('grant-expires').textContent).toContain('Expires on its own');
  });

  // Several sign-ins are one row, since "let this app in" is the decision
  // a person made -- but the count is shown, because it is also what a
  // client stuck in a sign-in loop looks like.
  it('shows one row per app, with the number of sign-ins', async () => {
    list.mockResolvedValue({ grants: [grant({ grants: 3 })] });
    await renderSection();
    await waitFor(() => expect(screen.getByTestId('grant-client_abc')).toBeTruthy());
    expect(screen.getAllByTestId(/^grant-/)).toHaveLength(1);
    expect(screen.getByTestId('grant-client_abc').textContent).toContain('3 sign-ins');
  });

  // A new account has nothing connected, and a deployment without OAuth2
  // does not serve this at all. Neither is worth a red box.
  it('renders nothing when there is nothing holding access', async () => {
    list.mockResolvedValue({ grants: [] });
    const { container } = await renderSection();
    await waitFor(() => expect(list).toHaveBeenCalled());
    expect(container.textContent).toBe('');
  });

  it('renders nothing when the endpoint is not served', async () => {
    list.mockRejectedValue(new Error('404 page not found'));
    const { container } = await renderSection();
    // Waited for rather than asserted straight away: the first render is
    // the loading state, and the refusal has not landed yet.
    await waitFor(() => expect(container.textContent).toBe(''));
  });
});
