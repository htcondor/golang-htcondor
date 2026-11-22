import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ConnectorStatus } from '@/lib/api';

// The third section. Two behaviours matter: it disappears rather than
// erroring where the deployment has no connectors, and connecting is a
// navigation rather than a fetch.

const list = vi.fn();
const disconnect = vi.fn();

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      connect: { list: () => list(), disconnect: (id: string) => disconnect(id) },
    },
  };
});

function connector(over: Partial<ConnectorStatus> = {}): ConnectorStatus {
  return {
    id: 'builtin:gdrive',
    kind: 'oauth2',
    connected: false,
    connect_url: '/api/v1/connect/builtin:gdrive',
    transfer: 'spool',
    ...over,
  };
}

async function renderSection() {
  const { ConnectedServicesSection } = await import('./ConnectedServicesSection');
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ConnectedServicesSection />
    </QueryClientProvider>,
  );
}

describe('the connected services section', () => {
  beforeEach(() => {
    list.mockReset();
    disconnect.mockReset();
  });

  it('renders nothing where the deployment configures no connectors', async () => {
    list.mockResolvedValue([]);
    const { container } = await renderSection();
    await waitFor(() => expect(list).toHaveBeenCalled());
    // Not an empty table and not a notice: this part of the page simply is
    // not in this deployment.
    expect(container.textContent).toBe('');
  });

  it('renders nothing when the endpoints are not served', async () => {
    // Multi-AP mode answers 501 here. A red box would read as something
    // broken, when the answer is that the feature is not available.
    list.mockRejectedValue(new Error('not available in multi-AP mode yet'));
    const { container } = await renderSection();
    // Wait for the failure to land, not just for the call: the loading line
    // is on screen in between.
    await waitFor(() => expect(container.textContent).toBe(''));
    expect(screen.queryByText(/multi-AP/)).not.toBeInTheDocument();
  });

  it('says which way the access runs', async () => {
    list.mockResolvedValue([connector()]);
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText('Services you connected')).toBeInTheDocument(),
    );
    // The distinguishing fact: we hold a credential on their account, and
    // nothing here can see their jobs.
    expect(screen.getByText(/Nothing here can see your jobs/)).toBeInTheDocument();
  });

  it('offers a link to connect, not a fetch', async () => {
    list.mockResolvedValue([connector()]);
    await renderSection();
    const link = await screen.findByRole('link', { name: 'Connect' });
    // The flow ends at the provider's consent screen, so it has to be a
    // navigation; an onClick that fetched would get a redirect it cannot use.
    expect(link).toHaveAttribute('href', '/api/v1/connect/builtin:gdrive');
  });

  it('uses the friendly provider name and keeps the id visible', async () => {
    list.mockResolvedValue([connector({ connected: true })]);
    await renderSection();
    await waitFor(() => expect(screen.getByText('Google Drive')).toBeInTheDocument());
    // A site may configure two connectors to one provider, so the id stays.
    expect(screen.getByText('builtin:gdrive')).toBeInTheDocument();
  });

  it('disconnects a connected account', async () => {
    list.mockResolvedValue([connector({ connected: true })]);
    disconnect.mockResolvedValue(undefined);
    await renderSection();

    await waitFor(() => expect(screen.getByText('Connected')).toBeInTheDocument());
    // ConfirmButton's accessible name is its title, not its label -- the
    // label is what the button SAYS, and the title is what it does.
    fireEvent.click(screen.getByRole('button', { name: /Forget this account/ }));
    // It arms first, so the destructive call needs the second click.
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(disconnect).toHaveBeenCalledWith('builtin:gdrive'));
  });

  it('distinguishes a grant the provider will not renew', async () => {
    list.mockResolvedValue([connector({ connected: true, needs_consent: true })]);
    await renderSection();
    // Different from never having connected, and reconnecting is the only
    // thing that fixes it.
    await waitFor(() =>
      expect(screen.getByText(/Needs reconnecting/)).toBeInTheDocument(),
    );
    expect(screen.getByRole('link', { name: 'Reconnect' })).toBeInTheDocument();
  });

  it('shows a dash rather than a blank for an account never used', async () => {
    list.mockResolvedValue([connector({ connected: true })]);
    await renderSection();
    await waitFor(() => expect(screen.getByText('—')).toBeInTheDocument());
  });
});
