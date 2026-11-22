import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Integration, IntegrationEndpoints } from '@/lib/api';

// What the page asks for, and what it shows once it is answered. The scope
// defaulting is the part worth a test: it lives only here, so nothing on the
// server side would catch its loss.

const list = vi.fn();
const create = vi.fn();
const remove = vi.fn();

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      integrations: {
        list: () => list(),
        create: (b: unknown) => create(b),
        remove: (id: string) => remove(id),
      },
    },
  };
});

const endpoints: IntegrationEndpoints = {
  issuer: 'https://ap.example.org',
  authorization_endpoint: 'https://ap.example.org/mcp/oauth2/authorize',
  token_endpoint: 'https://ap.example.org/mcp/oauth2/token',
  metadata_endpoint: 'https://ap.example.org/.well-known/oauth-authorization-server',
};

async function renderPage() {
  const Page = (await import('./page')).default;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Page />
    </QueryClientProvider>,
  );
}

describe('the Connectors page', () => {
  beforeEach(() => {
    list.mockReset();
    create.mockReset();
    remove.mockReset();
    list.mockResolvedValue({ integrations: [], endpoints });
  });

  it('says plainly which way the access runs', async () => {
    await renderPage();
    await waitFor(() =>
      expect(screen.getByText(/Apps connected to your account/)).toBeInTheDocument(),
    );
    // The direction matters more than the word "connector": an app connected
    // here acts as the user, which is the opposite of a connector the user
    // grants this server elsewhere.
    expect(screen.getByText(/can act as you on this access point/)).toBeInTheDocument();
  });

  it('shows an empty state rather than an empty table', async () => {
    await renderPage();
    await waitFor(() =>
      expect(screen.getByText(/No apps are connected/)).toBeInTheDocument(),
    );
  });

  it('always asks for offline_access, whatever the user ticked', async () => {
    create.mockResolvedValue({ id: 'client_abc', name: 'Zapier', public: false });
    await renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Connect an app' }));
    fireEvent.change(screen.getByPlaceholderText('Zapier'), { target: { value: 'Zapier' } });
    fireEvent.change(screen.getByPlaceholderText(/hooks.zapier.com/), { target: { value: 'https://hooks.zapier.com/cb' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    const body = create.mock.calls[0][0] as { scopes: string[]; name: string };
    // Without offline_access no refresh token is ever issued, and an
    // unattended integration stops working at the first token expiry.
    expect(body.scopes).toContain('offline_access');
    expect(body.scopes).toContain('mcp:read');
    expect(body.name).toBe('Zapier');
  });

  it('shows the credentials once, with the URLs the app will ask for', async () => {
    create.mockResolvedValue({
      id: 'client_abc',
      name: 'Zapier',
      public: false,
      client_secret: 's3cret-value',
      secret_shown_once: true,
      endpoints,
    });
    await renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Connect an app' }));
    fireEvent.change(screen.getByPlaceholderText('Zapier'), { target: { value: 'Zapier' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() =>
      expect(screen.getByDisplayValue('s3cret-value')).toBeInTheDocument(),
    );
    expect(screen.getByText(/never shown again/)).toBeInTheDocument();
    // The whole point of the panel: everything the setup form asks for, in
    // one place, at the one moment the secret is readable.
    expect(screen.getByDisplayValue(endpoints.token_endpoint)).toBeInTheDocument();
    expect(screen.getByDisplayValue('client_abc')).toBeInTheDocument();
  });

  it('reports a refusal instead of looking like it worked', async () => {
    create.mockRejectedValue(new Error('None of the requested scopes are available to your account'));
    await renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Connect an app' }));
    fireEvent.change(screen.getByPlaceholderText('Zapier'), { target: { value: 'Nope' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() =>
      expect(screen.getByText(/None of the requested scopes/)).toBeInTheDocument(),
    );
  });

  it('lists an app that has never been used as Never, not blank', async () => {
    const mine: Integration[] = [
      {
        id: 'client_xyz',
        name: 'Zapier',
        public: false,
        has_secret: true,
        created_at: new Date().toISOString(),
        scopes: ['mcp:read', 'offline_access'],
      },
    ];
    list.mockResolvedValue({ integrations: mine, endpoints });
    await renderPage();

    await waitFor(() => expect(screen.getByText('Zapier')).toBeInTheDocument());
    // "Never" is the answer somebody checks right after wiring an app up; a
    // blank cell reads as a page that failed to load the column.
    expect(screen.getByText('Never')).toBeInTheDocument();
    expect(screen.getByText('client_xyz')).toBeInTheDocument();
  });
});
