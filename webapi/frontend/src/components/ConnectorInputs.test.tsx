import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  applyConnectorInputs,
  ConnectorInputs,
  type ConnectorInput,
} from './ConnectorInputs';
import { chooseFiles, fetchPickers } from '@/lib/picker';

vi.mock('@/lib/picker', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/picker')>();
  return { ...actual, chooseFiles: vi.fn(), fetchPickers: vi.fn() };
});

const drive = {
  name: 'gdrive',
  provider: 'gdrive',
  connector: 'builtin:gdrive',
  service: 'webapi-builtin-gdrive',
  method: 'gdrive2',
  script: '/picker/gdrive.js',
  api_key: 'devkey',
  app_id: '1234',
};

function renderInputs() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <ConnectorInputs value={[]} onChange={() => {}} />
    </QueryClientProvider>,
  );
}

function input(reference: string, name: string): ConnectorInput {
  return { id: reference, reference, name, picker: 'drive' };
}

describe('applyConnectorInputs', () => {
  it('sets transfer_input_files when the body has none', () => {
    const out = applyConnectorInputs('executable = run.sh\n', [
      input('svc+gdrive2://ID/data.csv', 'data.csv'),
    ]);
    expect(out).toContain('transfer_input_files = svc+gdrive2://ID/data.csv');
  });

  // The body's own inputs survive. A template that already lists files
  // is the common case, and clobbering them would send a job without
  // the files it was written to use.
  it('appends to what the body already lists', () => {
    const out = applyConnectorInputs(
      'transfer_input_files = local.dat\n',
      [input('svc+gdrive2://ID/data.csv', 'data.csv')],
    );
    expect(out).toContain('local.dat, svc+gdrive2://ID/data.csv');
  });

  it('adds every chosen file', () => {
    const out = applyConnectorInputs('executable = run.sh\n', [
      input('svc+gdrive2://A/a.csv', 'a.csv'),
      input('svc+gdrive2://B/b.csv', 'b.csv'),
    ]);
    expect(out).toContain('svc+gdrive2://A/a.csv');
    expect(out).toContain('svc+gdrive2://B/b.csv');
  });

  // Nothing chosen must change nothing: the body is what the submitter
  // wrote, and an access point with no chooser configured goes through
  // this same call on every submit.
  it('leaves a body with nothing chosen exactly as it was', () => {
    const body = '# a comment\nexecutable = run.sh\nqueue\n';
    expect(applyConnectorInputs(body, [])).toBe(body);
  });

  it('does not list the same reference twice', () => {
    const ref = input('svc+gdrive2://ID/data.csv', 'data.csv');
    const out = applyConnectorInputs('executable = run.sh\n', [ref, ref]);
    expect(out.match(/svc\+gdrive2:\/\/ID\/data\.csv/g)).toHaveLength(1);
  });
});

// The button used to say "Opening..." for as long as the chooser was
// up, so a chooser that never rendered was indistinguishable from one
// the person was still using. Both states exist now, and they differ.
describe('ConnectorInputs while a chooser is open', () => {
  beforeEach(() => {
    vi.mocked(fetchPickers).mockResolvedValue([drive]);
    vi.mocked(chooseFiles).mockReset();
  });

  it('says Opening before the chooser appears, and Choosing once it has', async () => {
    let reportOpened: (() => void) | undefined;
    vi.mocked(chooseFiles).mockImplementation(
      (_p, hooks) =>
        new Promise(() => {
          reportOpened = hooks?.onOpened;
        }),
    );

    renderInputs();
    const button = await screen.findByRole('button', { name: /Add from/ });
    fireEvent.click(button);

    await screen.findByRole('button', { name: 'Opening…' });
    expect(reportOpened).toBeDefined();

    reportOpened!();
    await screen.findByRole('button', { name: 'Choosing…' });
  });

  it('surfaces the error when the chooser never opens', async () => {
    vi.mocked(chooseFiles).mockRejectedValue(
      new Error('the file chooser did not open.'),
    );

    renderInputs();
    fireEvent.click(await screen.findByRole('button', { name: /Add from/ }));

    await waitFor(() =>
      expect(screen.getByText(/did not open/)).toBeInTheDocument(),
    );
    // And the button is usable again, rather than stuck.
    await screen.findByRole('button', { name: /Add from/ });
  });
});

// Authorizing used to replace the submit page, which threw away the
// template, the table and everything else already filled in -- so the
// only safe route was to visit the Connectors page FIRST and come back.
// It opens a tab of its own now.
describe('ConnectorInputs when the account is not connected yet', () => {
  beforeEach(() => {
    vi.mocked(fetchPickers).mockResolvedValue([drive]);
    vi.mocked(chooseFiles).mockReset();
  });

  it('offers authorization in a new tab, so the batch being described survives', async () => {
    vi.mocked(chooseFiles).mockResolvedValue({
      kind: 'needs-connect',
      connectUrl: '/api/v1/connect/builtin:gdrive',
      needsConsent: false,
    });

    renderInputs();
    fireEvent.click(await screen.findByRole('button', { name: /Add from/ }));

    const link = await screen.findByRole('link', { name: 'Authorize it' });
    expect(link).toHaveAttribute('href', '/api/v1/connect/builtin:gdrive');
    expect(link).toHaveAttribute('target', '_blank');
    // Without noopener the new tab can navigate this one, which is the
    // same loss of work by another route.
    expect(link.getAttribute('rel')).toContain('noopener');
  });
});
