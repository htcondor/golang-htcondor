import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AdminConnector,
  AdminConnectorCatalogEntry,
  AdminConnectorRequest,
} from '@/lib/api';

// The form, which is the part of this page that was asking an operator to
// type things the server already knows.
//
// Each test here pins one of those: the namespace is chosen and never
// typed, the endpoints come from the provider, the group box is a pill
// field rather than a comma-separated string, and the two Drive-shaped
// controls are absent for providers that do not have them.

const listConnectors = vi.fn();
const connectorCatalog = vi.fn();
const discoverIssuer = vi.fn();
const saveConnector = vi.fn();

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      admin: {
        ...actual.api.admin,
        listConnectors: () => listConnectors(),
        connectorCatalog: () => connectorCatalog(),
        discoverIssuer: (issuer: string) => discoverIssuer(issuer),
        saveConnector: (req: AdminConnectorRequest) => saveConnector(req),
      },
    },
  };
});

function entry(over: Partial<AdminConnectorCatalogEntry> = {}): AdminConnectorCatalogEntry {
  return {
    id: 'builtin:gdrive',
    name: 'gdrive',
    kind: 'oauth2',
    summary: 'Google Drive, limited to files the user picks',
    default_scopes: ['https://www.googleapis.com/auth/drive.file'],
    transfer_method: 'gdrive',
    has_picker: true,
    picker_needs_api_key: true,
    configured: false,
    ...over,
  };
}

// The catalogue as this server actually ships it: Drive with both a
// chooser and a plugin, Dropbox with neither, Box with a plugin alone.
const SHIPPED: AdminConnectorCatalogEntry[] = [
  entry(),
  entry({
    id: 'builtin:box',
    name: 'box',
    summary: 'Box',
    default_scopes: undefined,
    transfer_method: 'box',
    has_picker: false,
    picker_needs_api_key: undefined,
  }),
  entry({
    id: 'builtin:dropbox',
    name: 'dropbox',
    summary: 'Dropbox (no transfer plugin; spool only)',
    default_scopes: undefined,
    transfer_method: undefined,
    has_picker: false,
    picker_needs_api_key: undefined,
  }),
];

function row(over: Partial<AdminConnector> = {}): AdminConnector {
  return {
    id: 'builtin:gdrive',
    kind: 'oauth2',
    origin: 'stored',
    stored_here: true,
    editable: true,
    enabled: true,
    groups: ['smith_lab'],
    scopes: [],
    transfer: 'spool',
    client_id: '1234.apps.googleusercontent.com',
    secret_set: true,
    users: 0,
    ...over,
  };
}

async function renderPage() {
  const Page = (await import('./page')).default;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Page />
    </QueryClientProvider>,
  );
}

describe('the admin connectors form', () => {
  beforeEach(() => {
    listConnectors.mockReset().mockResolvedValue([]);
    connectorCatalog.mockReset().mockResolvedValue({ builtins: SHIPPED });
    discoverIssuer.mockReset();
    saveConnector.mockReset().mockResolvedValue({ id: 'x', saved: true });
  });

  // The whole point of the catalogue. Choosing a service and saving must
  // produce a well-formed id without anybody having typed `builtin:`.
  it('builds the id from a chosen service rather than a typed prefix', async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin')).toBeTruthy());

    // No free-text id field at all: that is the control this replaces.
    expect(screen.queryByTestId('connector-id')).toBeNull();

    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:gdrive' },
    });
    fireEvent.change(screen.getByTestId('connector-client-id'), { target: { value: 'abc' } });
    fireEvent.change(screen.getByTestId('connector-groups'), { target: { value: 'smith_lab' } });
    fireEvent.keyDown(screen.getByTestId('connector-groups'), { key: 'Enter' });
    fireEvent.click(screen.getByTestId('connector-save'));

    await waitFor(() => expect(saveConnector).toHaveBeenCalled());
    expect(saveConnector.mock.calls[0][0]).toMatchObject({
      id: 'builtin:gdrive',
      client_id: 'abc',
      groups: ['smith_lab'],
    });
  });

  // The site half of the same thing: the namespace is shown as a fixed
  // prefix and the operator types only the name.
  it('prefixes a site connector itself', async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-namespace-site')).toBeTruthy());
    fireEvent.click(screen.getByTestId('connector-namespace-site'));

    fireEvent.change(screen.getByTestId('connector-site-name'), { target: { value: 'cryoem' } });
    fireEvent.change(screen.getByTestId('connector-client-id'), { target: { value: 'abc' } });
    fireEvent.click(screen.getByTestId('connector-everyone'));
    fireEvent.click(screen.getByTestId('connector-save'));

    await waitFor(() => expect(saveConnector).toHaveBeenCalled());
    expect(saveConnector.mock.calls[0][0].id).toBe('site:cryoem');
  });

  // Already configured stays listed, and is not selectable. Leaving it out
  // would make an operator wonder whether the server supports it at all;
  // offering it produces a save the server refuses.
  it('shows an already-configured service without offering it', async () => {
    connectorCatalog.mockResolvedValue({
      builtins: [entry({ configured: true, configured_origin: 'stored' }), SHIPPED[1]],
    });
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin')).toBeTruthy());

    const options = Array.from(
      screen.getByTestId('connector-builtin').querySelectorAll('option'),
    );
    const drive = options.find((o) => o.value === 'builtin:gdrive');
    expect(drive).toBeTruthy();
    expect(drive?.disabled).toBe(true);
    expect(drive?.textContent).toContain('already configured');
    // And the one that is not configured is still choosable, so this is
    // not just "every option is disabled".
    expect(options.find((o) => o.value === 'builtin:box')?.disabled).toBe(false);
  });

  // Discovery fills the endpoints. The operator types an issuer; the two
  // URLs they would otherwise transcribe never get typed.
  it('fills the endpoints from an issuer', async () => {
    discoverIssuer.mockResolvedValue({
      document: 'https://login.example.edu/.well-known/openid-configuration',
      issuer: 'https://login.example.edu',
      authorization_url: 'https://login.example.edu/auth',
      token_url: 'https://login.example.edu/token',
      scopes_supported: ['openid', 'files.read'],
    });
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-namespace-site')).toBeTruthy());
    fireEvent.click(screen.getByTestId('connector-namespace-site'));
    fireEvent.change(screen.getByTestId('connector-site-name'), { target: { value: 'lab' } });

    fireEvent.change(screen.getByTestId('connector-issuer'), {
      target: { value: 'https://login.example.edu' },
    });
    fireEvent.click(screen.getByTestId('connector-discover'));

    await waitFor(() => expect(screen.getByTestId('connector-discovered')).toBeTruthy());
    fireEvent.click(screen.getByTestId('connector-manual-toggle'));
    expect((screen.getByTestId('connector-auth-url') as HTMLInputElement).value).toBe(
      'https://login.example.edu/auth',
    );
    expect((screen.getByTestId('connector-token-url') as HTMLInputElement).value).toBe(
      'https://login.example.edu/token',
    );
  });

  // A warning is the whole value of discovery for a document that answered
  // but says something that fails later. It has to reach the page.
  it('shows what the discovered document says that will fail later', async () => {
    discoverIssuer.mockResolvedValue({
      document: 'https://login.example.edu/.well-known/openid-configuration',
      authorization_url: 'https://login.example.edu/auth',
      token_url: 'https://login.example.edu/token',
      warnings: ['this provider accepts only private_key_jwt at its token endpoint'],
    });
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-namespace-site')).toBeTruthy());
    fireEvent.click(screen.getByTestId('connector-namespace-site'));
    fireEvent.change(screen.getByTestId('connector-site-name'), { target: { value: 'lab' } });
    fireEvent.change(screen.getByTestId('connector-issuer'), {
      target: { value: 'https://login.example.edu' },
    });
    fireEvent.click(screen.getByTestId('connector-discover'));

    await waitFor(() => expect(screen.getByTestId('connector-discover-warning')).toBeTruthy());
    expect(screen.getByTestId('connector-discover-warning').textContent).toContain(
      'private_key_jwt',
    );
  });

  // Transfer is a choice only where there is something to choose. Drive has
  // a plugin, Dropbox has none, and a site provider never has one -- and the
  // control has to actually move, not merely be present for Drive.
  it('offers Transfer only where a transfer plugin ships', async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin')).toBeTruthy());

    // Nothing chosen: no provider, so no mode to pick.
    expect(screen.queryByTestId('connector-transfer')).toBeNull();

    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:gdrive' },
    });
    expect(screen.getByTestId('connector-transfer')).toBeTruthy();

    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:dropbox' },
    });
    expect(screen.queryByTestId('connector-transfer')).toBeNull();

    fireEvent.click(screen.getByTestId('connector-namespace-site'));
    fireEvent.change(screen.getByTestId('connector-site-name'), { target: { value: 'lab' } });
    expect(screen.queryByTestId('connector-transfer')).toBeNull();
  });

  // And a connector with no plugin is saved as spool, not as whatever the
  // hidden control happened to hold.
  it('saves a provider with no plugin as spool', async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin')).toBeTruthy());

    // Pick Drive, switch the mode to direct, then change provider. The
    // stale `direct` is the bug this guards.
    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:gdrive' },
    });
    fireEvent.change(screen.getByTestId('connector-transfer'), { target: { value: 'direct' } });
    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:dropbox' },
    });
    fireEvent.change(screen.getByTestId('connector-client-id'), { target: { value: 'abc' } });
    fireEvent.click(screen.getByTestId('connector-everyone'));
    fireEvent.click(screen.getByTestId('connector-save'));

    await waitFor(() => expect(saveConnector).toHaveBeenCalled());
    expect(saveConnector.mock.calls[0][0].transfer).toBe('spool');
  });

  // The chooser note is a Google Drive fact. Under any other provider it
  // sends an operator looking for a setting that does not exist.
  it('mentions the file chooser only under a provider that has one', async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin')).toBeTruthy());

    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:gdrive' },
    });
    expect(screen.getByTestId('connector-picker-note').textContent).toContain('developer key');

    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:box' },
    });
    expect(screen.queryByTestId('connector-picker-note')).toBeNull();
  });

  // Groups are pills, and a save with none is refused here rather than at
  // the server -- a connector nobody may use looks identical to a broken
  // one from outside.
  it('will not save a connector nobody may use', async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin')).toBeTruthy());
    fireEvent.change(screen.getByTestId('connector-builtin'), {
      target: { value: 'builtin:gdrive' },
    });
    fireEvent.change(screen.getByTestId('connector-client-id'), { target: { value: 'abc' } });

    expect((screen.getByTestId('connector-save') as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId('connector-groups-error')).toBeTruthy();

    fireEvent.change(screen.getByTestId('connector-groups'), { target: { value: 'smith_lab' } });
    fireEvent.keyDown(screen.getByTestId('connector-groups'), { key: 'Enter' });
    expect(screen.getByTestId('group-pill-smith_lab')).toBeTruthy();
    expect((screen.getByTestId('connector-save') as HTMLButtonElement).disabled).toBe(false);
  });

  // Editing used to open a blank form whose Save wrote the blanks, which
  // emptied the groups of a working connector.
  it('loads the existing values when editing', async () => {
    listConnectors.mockResolvedValue([
      row({ groups: ['smith_lab', 'jones_lab'], transfer: 'direct' }),
    ]);
    await renderPage();
    await waitFor(() => expect(screen.getByTestId('connector-builtin:gdrive')).toBeTruthy());

    fireEvent.click(screen.getByText('Edit'));
    await waitFor(() => expect(screen.getByTestId('group-pill-smith_lab')).toBeTruthy());
    expect(screen.getByTestId('group-pill-jones_lab')).toBeTruthy();
    expect((screen.getByTestId('connector-client-id') as HTMLInputElement).value).toBe(
      '1234.apps.googleusercontent.com',
    );
    expect((screen.getByTestId('connector-transfer') as HTMLSelectElement).value).toBe('direct');

    // And saving keeps them, rather than writing the blanks.
    fireEvent.click(screen.getByTestId('connector-save'));
    await waitFor(() => expect(saveConnector).toHaveBeenCalled());
    expect(saveConnector.mock.calls[0][0]).toMatchObject({
      id: 'builtin:gdrive',
      groups: ['smith_lab', 'jones_lab'],
      transfer: 'direct',
    });
    // No secret field was touched, so none is sent: an empty string would
    // clear the stored one.
    expect(saveConnector.mock.calls[0][0].client_secret).toBeUndefined();
  });
});
