import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Webhook } from '@/lib/api';

// The notifications panel. Two behaviours are load-bearing: the destination
// is never rendered back, and Test reports what the receiver actually said.

const list = vi.fn();
const create = vi.fn();
const test_ = vi.fn();
const enable = vi.fn();
const remove = vi.fn();
const deliveries = vi.fn();

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      webhooks: {
        list: () => list(),
        create: (b: unknown) => create(b),
        update: () => Promise.reject(new Error('not used here')),
        remove: (id: string) => remove(id),
        test: (id: string) => test_(id),
        enable: (id: string) => enable(id),
        deliveries: (id: string) => deliveries(id),
      },
    },
  };
});

const EVENTS = ['done', 'succeeded', 'failed', 'held', 'running'];

function hook(over: Partial<Webhook> = {}): Webhook {
  return {
    id: 'wh_1',
    label: 'Slack #jobs',
    scope: 'owner',
    events: ['failed'],
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    consecutive_failures: 0,
    has_destination: true,
    destination_hint: 'https://hooks.slack.com/… (46 more characters)',
    ...over,
  };
}

async function renderSection() {
  const { WebhooksSection } = await import('./WebhooksSection');
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <WebhooksSection />
    </QueryClientProvider>,
  );
}

describe('the event notifications panel', () => {
  beforeEach(() => {
    list.mockReset();
    create.mockReset();
    test_.mockReset();
    enable.mockReset();
    remove.mockReset();
    deliveries.mockReset();
    list.mockResolvedValue({ webhooks: [], events: EVENTS });
    deliveries.mockResolvedValue({ deliveries: [] });
  });

  it('says which way the access runs', async () => {
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText('Event notifications')).toBeInTheDocument(),
    );
    // Nothing outside gains access here: we reach out to it. Mixing that up
    // with the apps section is the mistake the headings exist to prevent.
    expect(screen.getByText(/posts to a URL you give it/)).toBeInTheDocument();
  });

  it('shows an empty state rather than an empty list', async () => {
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/No event notifications are set up/)).toBeInTheDocument(),
    );
  });

  it('never renders the destination back, only its host', async () => {
    const secretPath = 'https://hooks.slack.com/services/T0/B0/SUPERSECRETTOKEN';
    list.mockResolvedValue({ webhooks: [hook()], events: EVENTS });
    await renderSection();
    await waitFor(() => expect(screen.getByText('Slack #jobs')).toBeInTheDocument());
    // For most services holding the URL is the authorization to post, so the
    // page is given a hint and never the value.
    expect(document.body.textContent).not.toContain(secretPath);
    expect(document.body.textContent).not.toContain('SUPERSECRETTOKEN');
    expect(screen.getByText(/hooks\.slack\.com/)).toBeInTheDocument();
  });

  it('sends what the form describes, parsing a job as cluster.proc', async () => {
    create.mockResolvedValue({
      webhook: hook({ scope: 'job' }),
      signing_secret: 'deadbeef',
      secret_shown_once: true,
      signature_header: 'X-HTCondor-Signature',
      idempotency_header: 'Idempotency-Key',
    });
    await renderSection();

    fireEvent.click(await screen.findByRole('button', { name: 'Add a notification' }));
    fireEvent.change(screen.getByPlaceholderText('Slack #jobs'), {
      target: { value: 'My Zap' },
    });
    fireEvent.change(screen.getByPlaceholderText(/hooks.slack.com/), {
      target: { value: 'https://hooks.zapier.com/abc' },
    });
    fireEvent.click(screen.getByRole('radio', { name: /one job/ }));
    fireEvent.change(screen.getByPlaceholderText('1234.0'), {
      target: { value: '4242.7' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0][0]).toMatchObject({
      label: 'My Zap',
      url: 'https://hooks.zapier.com/abc',
      scope: 'job',
      cluster_id: 4242,
      proc_id: 7,
      events: ['failed'],
    });
  });

  it('treats a bare number as a cluster batch and a word as a batch name', async () => {
    create.mockResolvedValue({
      webhook: hook({ scope: 'batch' }),
      signing_secret: 'x',
      secret_shown_once: true,
      signature_header: 'X-HTCondor-Signature',
      idempotency_header: 'Idempotency-Key',
    });
    await renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Add a notification' }));
    fireEvent.change(screen.getByPlaceholderText('Slack #jobs'), { target: { value: 'b' } });
    fireEvent.change(screen.getByPlaceholderText(/hooks.slack.com/), {
      target: { value: 'https://h.example.org/x' },
    });
    fireEvent.click(screen.getByRole('radio', { name: /one batch/ }));
    fireEvent.change(screen.getByPlaceholderText(/cluster number/), {
      target: { value: '991' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0][0]).toMatchObject({ batch_id: '991' });
    expect(create.mock.calls[0][0].batch_name).toBeUndefined();

    create.mockClear();
    fireEvent.change(screen.getByPlaceholderText('Slack #jobs'), { target: { value: 'b2' } });
    fireEvent.change(screen.getByPlaceholderText(/hooks.slack.com/), {
      target: { value: 'https://h.example.org/x' },
    });
    fireEvent.change(screen.getByPlaceholderText(/cluster number/), {
      target: { value: 'nightly' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0][0]).toMatchObject({ batch_name: 'nightly' });
  });

  it('shows the signing secret once, and says what it is for', async () => {
    create.mockResolvedValue({
      webhook: hook(),
      signing_secret: 'abc123secret',
      secret_shown_once: true,
      signature_header: 'X-HTCondor-Signature',
      idempotency_header: 'Idempotency-Key',
    });
    await renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Add a notification' }));
    fireEvent.change(screen.getByPlaceholderText('Slack #jobs'), { target: { value: 'x' } });
    fireEvent.change(screen.getByPlaceholderText(/hooks.slack.com/), {
      target: { value: 'https://h.example.org/x' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() =>
      expect(screen.getByDisplayValue('abc123secret')).toBeInTheDocument(),
    );
    expect(screen.getByText(/never shown again/)).toBeInTheDocument();
    // A receiver author needs to know what the signature covers, not just
    // that there is one.
    expect(screen.getByText(/X-HTCondor-Signature/)).toBeInTheDocument();
  });

  it('reports what the receiver said when a test fails', async () => {
    list.mockResolvedValue({ webhooks: [hook()], events: EVENTS });
    test_.mockResolvedValue({
      ok: false,
      status: 404,
      error: 'the destination answered 404 Not Found: no such hook',
    });
    await renderSection();

    fireEvent.click(await screen.findByRole('button', { name: 'Test' }));
    await waitFor(() => expect(test_).toHaveBeenCalledWith('wh_1'));
    // The receiver's own words: "it didn't work" is not actionable.
    await waitFor(() =>
      expect(screen.getByText(/no such hook/)).toBeInTheDocument(),
    );
    expect(screen.getByText(/Not delivered \(404\)/)).toBeInTheDocument();
  });

  it('confirms a successful test', async () => {
    list.mockResolvedValue({ webhooks: [hook()], events: EVENTS });
    test_.mockResolvedValue({ ok: true, status: 200 });
    await renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Test' }));
    await waitFor(() =>
      expect(screen.getByText(/your receiver answered 200/)).toBeInTheDocument(),
    );
  });

  it('explains a disabled hook and offers to re-enable it', async () => {
    list.mockResolvedValue({
      webhooks: [
        hook({
          disabled_at: new Date().toISOString(),
          disabled_reason: 'disabled after 5 deliveries in a row were given up on',
        }),
      ],
      events: EVENTS,
    });
    await renderSection();
    // Disabled rather than quietly failing, because silence reads as
    // "nothing has happened yet".
    await waitFor(() =>
      expect(screen.getByText(/5 deliveries in a row/)).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Re-enable' }));
    await waitFor(() => expect(enable).toHaveBeenCalledWith('wh_1'));
  });

  it('says so when a hook was capped, rather than leaving the gap unexplained', async () => {
    list.mockResolvedValue({
      webhooks: [hook({ capped_at: new Date().toISOString(), capped_jobs: 10_000 })],
      events: EVENTS,
    });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/10000 jobs matched at once/)).toBeInTheDocument(),
    );
  });

  it('calls a never-delivered hook never, not blank', async () => {
    list.mockResolvedValue({ webhooks: [hook()], events: EVENTS });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/Last delivery: never/)).toBeInTheDocument(),
    );
  });

  it('loads recent attempts only when asked', async () => {
    list.mockResolvedValue({ webhooks: [hook()], events: EVENTS });
    deliveries.mockResolvedValue({
      deliveries: [
        { id: 'd1', event: 'failed', state: 'abandoned', attempts: 1, status: 404, error: 'gone', job: '42.0' },
      ],
    });
    await renderSection();
    await waitFor(() => expect(screen.getByText('Slack #jobs')).toBeInTheDocument());
    // A query per hook on every page load would be a query per hook for
    // something nobody looked at.
    expect(deliveries).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Recent attempts' }));
    await waitFor(() => expect(deliveries).toHaveBeenCalledWith('wh_1'));
    // "given up" rather than "failed": we stopped trying, which is a
    // different thing to report.
    await waitFor(() => expect(screen.getByText(/given up: gone/)).toBeInTheDocument());
  });
});
