import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Webhook, WebhookSubscription } from '@/lib/api';

// The notifications panel. Three behaviours are load-bearing: the
// destination is never rendered back, Test reports what the receiver
// actually said, and adding a second subscription asks for no URL and no
// secret -- which is the whole reason destinations and subscriptions are
// separate things.

const list = vi.fn();
const create = vi.fn();
const test_ = vi.fn();
const remove = vi.fn();
const deliveries = vi.fn();
const rotateSecret = vi.fn();
const listSubs = vi.fn();
const createSub = vi.fn();
const removeSub = vi.fn();

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
        deliveries: (id: string) => deliveries(id),
        rotateSecret: (id: string) => rotateSecret(id),
      },
      webhookSubscriptions: {
        list: () => listSubs(),
        create: (b: unknown) => createSub(b),
        update: () => Promise.reject(new Error('not used here')),
        remove: (id: string) => removeSub(id),
      },
    },
  };
});

const EVENTS = ['done', 'succeeded', 'failed', 'held', 'running'];
const MODES = ['summary', 'full'];

function dest(over: Partial<Webhook> = {}): Webhook {
  return {
    id: 'wh_1',
    label: 'Slack #jobs',
    payload: 'summary',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    consecutive_failures: 0,
    has_destination: true,
    destination_hint: 'https://hooks.slack.com/… (46 more characters)',
    ...over,
  };
}

function sub(over: Partial<WebhookSubscription> = {}): WebhookSubscription {
  return {
    id: 'ws_1',
    endpoint_id: 'wh_1',
    label: '',
    scope: 'owner',
    events: ['failed'],
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
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
    remove.mockReset();
    deliveries.mockReset();
    rotateSecret.mockReset();
    listSubs.mockReset();
    createSub.mockReset();
    removeSub.mockReset();
    list.mockResolvedValue({ webhooks: [], events: EVENTS, payload_modes: MODES });
    listSubs.mockResolvedValue({ subscriptions: [], events: EVENTS });
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
      expect(screen.getByText(/No destinations yet/)).toBeInTheDocument(),
    );
  });

  it('never renders the destination back, only its host', async () => {
    const secretPath = 'https://hooks.slack.com/services/T0/B0/SUPERSECRETTOKEN';
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    await renderSection();
    await waitFor(() => expect(screen.getByText('Slack #jobs')).toBeInTheDocument());
    // For most services holding the URL is the authorization to post, so the
    // page is given a hint and never the value.
    expect(document.body.textContent).not.toContain(secretPath);
    expect(document.body.textContent).not.toContain('SUPERSECRETTOKEN');
    expect(screen.getByText(/hooks\.slack\.com/)).toBeInTheDocument();
  });

  it('asks for no URL and no secret when adding a second subscription', async () => {
    // The point of the split. With a destination already registered, the
    // "what to watch" form must not contain a URL field at all.
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    createSub.mockResolvedValue({ subscription: sub() });
    await renderSection();

    fireEvent.click(
      await screen.findByRole('button', { name: 'Notify me about something' }),
    );
    expect(screen.queryByPlaceholderText(/hooks.slack.com/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(createSub).toHaveBeenCalledTimes(1));
    // The one destination is chosen for them rather than made a decision.
    expect(createSub.mock.calls[0][0]).toMatchObject({
      endpoint_id: 'wh_1',
      scope: 'owner',
      events: ['failed'],
    });
    // Nothing on this path mints a key.
    expect(create).not.toHaveBeenCalled();
  });

  it('offers nothing to watch until a destination exists', async () => {
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/No destinations yet/)).toBeInTheDocument(),
    );
    expect(
      screen.queryByRole('button', { name: 'Notify me about something' }),
    ).not.toBeInTheDocument();
  });

  it('sends what the subscription form describes, parsing a job as cluster.proc', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    createSub.mockResolvedValue({ subscription: sub({ scope: 'job' }) });
    await renderSection();

    fireEvent.click(
      await screen.findByRole('button', { name: 'Notify me about something' }),
    );
    fireEvent.click(screen.getByRole('radio', { name: /one job/ }));
    fireEvent.change(screen.getByPlaceholderText('1234.0'), {
      target: { value: '4242.7' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() => expect(createSub).toHaveBeenCalledTimes(1));
    expect(createSub.mock.calls[0][0]).toMatchObject({
      endpoint_id: 'wh_1',
      scope: 'job',
      cluster_id: 4242,
      proc_id: 7,
      events: ['failed'],
    });
  });

  it('clears the whole form after adding, not just the text', async () => {
    // A form that keeps its scope and job id is how somebody adds a second
    // subscription still pointed at the last one's job without noticing.
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    createSub.mockResolvedValue({ subscription: sub({ scope: 'job' }) });
    await renderSection();

    fireEvent.click(
      await screen.findByRole('button', { name: 'Notify me about something' }),
    );
    fireEvent.click(screen.getByRole('radio', { name: /one job/ }));
    fireEvent.change(screen.getByPlaceholderText('1234.0'), {
      target: { value: '4242.7' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(createSub).toHaveBeenCalledTimes(1));

    // Back to watching everything, with no job id left over.
    expect(screen.getByRole('radio', { name: /all my jobs/ })).toBeChecked();
    expect(screen.queryByPlaceholderText('1234.0')).not.toBeInTheDocument();
  });

  it('treats a bare number as a cluster batch and a word as a batch name', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    createSub.mockResolvedValue({ subscription: sub({ scope: 'batch' }) });
    await renderSection();
    fireEvent.click(
      await screen.findByRole('button', { name: 'Notify me about something' }),
    );
    fireEvent.click(screen.getByRole('radio', { name: /one batch/ }));
    fireEvent.change(screen.getByPlaceholderText(/cluster number/), {
      target: { value: '991' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(createSub).toHaveBeenCalledTimes(1));
    expect(createSub.mock.calls[0][0]).toMatchObject({ batch_id: '991' });
    expect(createSub.mock.calls[0][0].batch_name).toBeUndefined();

    createSub.mockClear();
    fireEvent.click(screen.getByRole('radio', { name: /one batch/ }));
    fireEvent.change(screen.getByPlaceholderText(/cluster number/), {
      target: { value: 'nightly' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(createSub).toHaveBeenCalledTimes(1));
    expect(createSub.mock.calls[0][0]).toMatchObject({ batch_name: 'nightly' });
  });

  it('shows the signing secret once, says what it is for, and says to decode it', async () => {
    create.mockResolvedValue({
      webhook: dest(),
      signing_secret: 'abc123secret',
      secret_shown_once: true,
      signature_header: 'X-HTCondor-Signature',
      idempotency_header: 'Idempotency-Key',
    });
    await renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Add a destination' }));
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
    // And the one thing that fails silently: the value is hex, and using it
    // undecoded still produces a well-formed signature that never matches.
    expect(screen.getByText(/decode it to 32 bytes/i)).toBeInTheDocument();
  });

  it('sends the payload mode with a new destination', async () => {
    create.mockResolvedValue({
      webhook: dest({ payload: 'full' }),
      signing_secret: 'x',
      secret_shown_once: true,
      signature_header: 'X-HTCondor-Signature',
      idempotency_header: 'Idempotency-Key',
    });
    await renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Add a destination' }));
    fireEvent.change(screen.getByPlaceholderText('Slack #jobs'), { target: { value: 'x' } });
    fireEvent.change(screen.getByPlaceholderText(/hooks.slack.com/), {
      target: { value: 'https://h.example.org/x' },
    });
    // Summary is the default, because sending the whole ad is a disclosure
    // decision and has to be made rather than inherited.
    expect(screen.getByRole('radio', { name: /A summary/ })).toBeChecked();
    fireEvent.click(screen.getByRole('radio', { name: /The whole job ad/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0][0]).toMatchObject({ payload: 'full' });
  });

  it('says which destinations send whole ads', async () => {
    list.mockResolvedValue({
      webhooks: [dest({ payload: 'full' })],
      events: EVENTS,
      payload_modes: MODES,
    });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/Sends the whole job ad/)).toBeInTheDocument(),
    );
  });

  it('reports what the receiver said when a test fails', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
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
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    test_.mockResolvedValue({ ok: true, status: 200 });
    await renderSection();
    fireEvent.click(await screen.findByRole('button', { name: 'Test' }));
    await waitFor(() =>
      expect(screen.getByText(/your receiver answered 200/)).toBeInTheDocument(),
    );
  });

  it('says a failing destination is waiting, not switched off', async () => {
    // Nothing is disabled any more. The row has to say that the queue is
    // intact, or a user reads the silence as lost notifications.
    list.mockResolvedValue({
      webhooks: [
        dest({
          consecutive_failures: 6,
          backoff_until: new Date(Date.now() + 10 * 60_000).toISOString(),
        }),
      ],
      events: EVENTS,
      payload_modes: MODES,
    });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/not answering/)).toBeInTheDocument(),
    );
    expect(screen.getByText(/6 failures in a row/)).toBeInTheDocument();
    expect(screen.getByText(/the queue drains/)).toBeInTheDocument();
    // There is nothing to re-enable, because nothing was disabled.
    expect(
      screen.queryByRole('button', { name: 'Re-enable' }),
    ).not.toBeInTheDocument();
  });

  it('says when a destination has dropped events for a full queue', async () => {
    list.mockResolvedValue({
      webhooks: [dest({ backlog_at: new Date().toISOString(), backlog_depth: 1000 })],
      events: EVENTS,
      payload_modes: MODES,
    });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/some events have been dropped/)).toBeInTheDocument(),
    );
  });

  it('says so when a subscription was capped, rather than leaving the gap unexplained', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    listSubs.mockResolvedValue({
      subscriptions: [sub({ capped_at: new Date().toISOString(), capped_jobs: 10_000 })],
      events: EVENTS,
    });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/10000 jobs matched at once/)).toBeInTheDocument(),
    );
  });

  it('shows which destination each subscription feeds', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    listSubs.mockResolvedValue({
      subscriptions: [sub({ scope: 'job', job: { cluster_id: 42, proc_id: 1 } })],
      events: EVENTS,
    });
    await renderSection();
    await waitFor(() => expect(screen.getByText('job 42.1')).toBeInTheDocument());
    expect(screen.getByText(/→ Slack #jobs/)).toBeInTheDocument();
    // And the destination says how much feeds it, so deleting one can warn.
    expect(screen.getByText('1 subscription')).toBeInTheDocument();
  });

  it('calls a never-delivered destination never, not blank', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/Last delivery: never/)).toBeInTheDocument(),
    );
  });

  it('loads recent attempts only when asked', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    deliveries.mockResolvedValue({
      deliveries: [
        { id: 'd1', event: 'failed', state: 'abandoned', attempts: 1, status: 404, error: 'gone', job: '42.0' },
      ],
    });
    await renderSection();
    await waitFor(() => expect(screen.getByText('Slack #jobs')).toBeInTheDocument());
    // A query per destination on every page load would be a query per
    // destination for something nobody looked at.
    expect(deliveries).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Recent attempts' }));
    await waitFor(() => expect(deliveries).toHaveBeenCalledWith('wh_1'));
    // "given up" rather than "failed": we stopped trying, which is a
    // different thing to report.
    await waitFor(() => expect(screen.getByText(/given up: gone/)).toBeInTheDocument());
  });
});

describe('rotating a signing secret', () => {
  beforeEach(() => {
    list.mockReset();
    createSub.mockReset();
    rotateSecret.mockReset();
    listSubs.mockReset();
    deliveries.mockReset();
    listSubs.mockResolvedValue({ subscriptions: [], events: EVENTS });
    deliveries.mockResolvedValue({ deliveries: [] });
  });

  it('shows the new secret once and says the old one still works', async () => {
    const until = new Date(Date.now() + 24 * 3600_000).toISOString();
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    rotateSecret.mockResolvedValue({
      webhook: dest({ prev_secret_expires_at: until }),
      signing_secret: 'newsecret123',
      secret_shown_once: true,
      signature_header: 'X-HTCondor-Signature',
      idempotency_header: 'Idempotency-Key',
    });
    await renderSection();

    // Two-click affordance: rotation is not undoable, so it is confirmed.
    // ConfirmButton's accessible name is its title, not its label.
    fireEvent.click(await screen.findByRole('button', { name: /Mint a new signing secret/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(rotateSecret).toHaveBeenCalledWith('wh_1'));

    await waitFor(() =>
      expect(screen.getByDisplayValue('newsecret123')).toBeInTheDocument(),
    );
    // The banner has to say this is a rotation, not a new destination.
    expect(screen.getByText(/has a new signing secret/)).toBeInTheDocument();
    // And that nothing is lost meanwhile, which is the whole point of
    // the overlap.
    expect(screen.getByText(/old secret keeps working until/)).toBeInTheDocument();
    expect(screen.getByText(/both signatures/)).toBeInTheDocument();
    // The escape hatch for a leaked key.
    expect(screen.getByText(/rotate again now/)).toBeInTheDocument();
  });

  it('shows the deadline on a destination mid-rotation', async () => {
    const until = new Date(Date.now() + 10 * 3600_000).toISOString();
    list.mockResolvedValue({
      webhooks: [dest({ prev_secret_expires_at: until })],
      events: EVENTS,
      payload_modes: MODES,
    });
    await renderSection();
    await waitFor(() =>
      expect(screen.getByText(/previous one still works until/)).toBeInTheDocument(),
    );
    expect(screen.getByText(/update your receiver before that time/)).toBeInTheDocument();
  });

  it('says nothing about rotation on a destination that has not rotated', async () => {
    list.mockResolvedValue({ webhooks: [dest()], events: EVENTS, payload_modes: MODES });
    await renderSection();
    await waitFor(() => expect(screen.getByText('Slack #jobs')).toBeInTheDocument());
    expect(screen.queryByText(/previous one still works/)).not.toBeInTheDocument();
  });
});
