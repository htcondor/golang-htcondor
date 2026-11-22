"use client";

// Event notifications: the outbound half.
//
// This server posts to a service when the user's jobs change state, which is
// the opposite direction to the apps section above it -- an app connected
// there acts as the user HERE, a webhook tells a service what happened here.
// Both headings say which way, because revoking the wrong one is the mistake
// the page exists to prevent.
//
// The panel is in two halves because the data is: a DESTINATION is where
// deliveries go and the key a receiver verifies with, and a SUBSCRIPTION is
// what is watched. They used to be one thing, which meant watching a second
// job required pasting the capability URL again and copying a second signing
// secret for the same receiver -- and the secret is shown once, so that was
// the most error-prone moment the feature had.
//
// Two things here are load-bearing rather than decorative. The signing
// secret is shown once and never again, so the moment it appears is the
// moment the user has to copy it. And Test sends a REAL delivery, so its
// result is the only honest answer to "is this configured right".

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type Webhook,
  type WebhookCreated,
  type WebhookSubscription,
  type WebhookTestResult,
} from "@/lib/api";
import { ChipList } from "@/components/ChipList";
import { ConfirmButton } from "@/components/ConfirmButton";

// What each event means, in the user's terms rather than the schedd's. The
// server decides which exist; this only supplies the wording.
const EVENT_HELP: Record<string, string> = {
  done: "Left the queue, whatever the outcome.",
  succeeded: "Finished with exit code 0.",
  failed: "Finished badly, was killed, or was removed.",
  held: "Went on hold and needs attention.",
  running: "Started executing.",
};

export function WebhooksSection() {
  const qc = useQueryClient();
  const [created, setCreated] = useState<WebhookCreated | null>(null);
  const [tested, setTested] = useState<Record<string, WebhookTestResult>>({});

  const dests = useQuery({
    queryKey: ["webhooks"],
    queryFn: api.webhooks.list,
  });
  const subs = useQuery({
    queryKey: ["webhook-subscriptions"],
    queryFn: api.webhookSubscriptions.list,
  });

  // A subscription changes nothing about a destination and vice versa, but
  // deleting a destination takes its subscriptions with it, so both lists
  // are refreshed after any write rather than reasoning about which.
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["webhooks"] });
    qc.invalidateQueries({ queryKey: ["webhook-subscriptions"] });
  };

  const createDest = useMutation({
    mutationFn: api.webhooks.create,
    onSuccess: (res) => {
      setCreated(res);
      refresh();
    },
  });

  const removeDest = useMutation({
    mutationFn: (id: string) => api.webhooks.remove(id),
    onSuccess: refresh,
  });

  const createSub = useMutation({
    mutationFn: api.webhookSubscriptions.create,
    onSuccess: refresh,
  });

  const removeSub = useMutation({
    mutationFn: (id: string) => api.webhookSubscriptions.remove(id),
    onSuccess: refresh,
  });

  const test = useMutation({
    mutationFn: (id: string) => api.webhooks.test(id),
    onSuccess: (res, id) => {
      setTested((cur) => ({ ...cur, [id]: res }));
      // The attempt is recorded against the destination, so the row's own
      // "last test" line comes from the server rather than from this state.
      qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });

  const destinations = dests.data?.webhooks ?? [];
  const subscriptions = subs.data?.subscriptions ?? [];
  const events = subs.data?.events ?? dests.data?.events ?? [];
  const loading = dests.isLoading || subs.isLoading;
  const failed = dests.error ?? subs.error;

  // How many things feed each destination, so a delete can say what it
  // takes with it rather than surprising somebody.
  const feeding = new Map<string, number>();
  for (const s of subscriptions) {
    feeding.set(s.endpoint_id, (feeding.get(s.endpoint_id) ?? 0) + 1);
  }

  return (
    <section className="space-y-6">
      <div>
        <h2 className="text-lg font-semibold text-gray-900">
          Event notifications
        </h2>
        <p className="mt-1 text-sm text-gray-500">
          This access point posts to a URL you give it when your jobs change
          state — for a Zap, a Slack channel, or anything that accepts a
          webhook. Set a destination up once; then say what you want to hear
          about, as often as you like, without configuring the receiver again.
        </p>
      </div>

      {created && (
        <NewWebhookShown created={created} onDismiss={() => setCreated(null)} />
      )}

      {loading && <p className="text-sm text-gray-500">Loading…</p>}
      {failed != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(failed as Error).message}
        </p>
      )}

      {!loading && failed == null && (
        <>
          <div className="space-y-3">
            <div>
              <h3 className="text-sm font-semibold text-gray-900">
                Where notifications go
              </h3>
              <p className="mt-0.5 text-xs text-gray-500">
                Each destination has its own signing secret, shown once when
                you add it. The URL is treated as a secret too, because for
                most services holding it is all it takes to post there.
              </p>
            </div>

            <CreateDestinationForm
              pending={createDest.isPending}
              error={createDest.error}
              onCreate={(body) => createDest.mutate(body)}
            />

            {destinations.length === 0 ? (
              <p className="rounded-lg border border-dashed border-gray-300 px-4 py-6 text-center text-sm text-gray-500">
                No destinations yet. Add one before choosing what to be
                notified about.
              </p>
            ) : (
              <div className="space-y-3">
                {destinations.map((d) => (
                  <DestinationRow
                    key={d.id}
                    dest={d}
                    feeds={feeding.get(d.id) ?? 0}
                    tested={tested[d.id]}
                    testing={test.isPending && test.variables === d.id}
                    onTest={() => test.mutate(d.id)}
                    onRemove={() => removeDest.mutate(d.id)}
                    removing={removeDest.isPending}
                  />
                ))}
              </div>
            )}
          </div>

          <div className="space-y-3">
            <div>
              <h3 className="text-sm font-semibold text-gray-900">
                What you are notified about
              </h3>
              <p className="mt-0.5 text-xs text-gray-500">
                Each of these watches one job, one batch, or everything you
                run, and posts to a destination above. Adding one asks for no
                URL and no secret.
              </p>
            </div>

            <CreateSubscriptionForm
              destinations={destinations}
              events={events}
              pending={createSub.isPending}
              error={createSub.error}
              onCreate={(body) => createSub.mutate(body)}
            />

            {subscriptions.length === 0 ? (
              <p className="rounded-lg border border-dashed border-gray-300 px-4 py-6 text-center text-sm text-gray-500">
                Nothing is being watched.
              </p>
            ) : (
              <div className="space-y-3">
                {subscriptions.map((s) => (
                  <SubscriptionRow
                    key={s.id}
                    sub={s}
                    dest={destinations.find((d) => d.id === s.endpoint_id)}
                    onRemove={() => removeSub.mutate(s.id)}
                    removing={removeSub.isPending}
                  />
                ))}
              </div>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function CreateDestinationForm({
  onCreate,
  pending,
  error,
}: {
  onCreate: (body: {
    label: string;
    url: string;
    payload?: "summary" | "full";
  }) => void;
  pending: boolean;
  error: unknown;
}) {
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState("");
  const [url, setUrl] = useState("");
  const [payload, setPayload] = useState<"summary" | "full">("summary");

  const ready = label.trim() !== "" && url.trim() !== "";

  // Everything is cleared, not just the text fields. A form that keeps its
  // previous choices is how somebody adds a second destination that quietly
  // carries the first one's settings.
  const reset = () => {
    setLabel("");
    setUrl("");
    setPayload("summary");
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700"
      >
        Add a destination
      </button>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border border-gray-200 bg-gray-50 p-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block">
          <span className="text-xs font-medium text-gray-700">Name</span>
          <input
            type="text"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="Slack #jobs"
            className="mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm text-gray-900"
          />
        </label>
        <label className="block">
          <span className="text-xs font-medium text-gray-700">
            Destination URL
          </span>
          <input
            type="text"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://hooks.slack.com/services/…"
            className="mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm text-gray-900"
          />
          <span className="mt-1 block text-xs text-gray-500">
            Must be https. Kept secret and never shown back to you.
          </span>
        </label>
      </div>

      <fieldset>
        <legend className="text-xs font-medium text-gray-700">
          How much to send
        </legend>
        <div className="mt-1 space-y-1">
          {(
            [
              [
                "summary",
                "A summary",
                "Job id, owner, status, exit code, hold reason, command. Enough for most receivers.",
              ],
              [
                "full",
                "The whole job ad",
                "Adds every attribute of the job beside the summary — including arguments and anything custom you set. Send it only where you would be comfortable with all of that.",
              ],
            ] as const
          ).map(([value, title, help]) => (
            <label key={value} className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="payload"
                checked={payload === value}
                onChange={() => setPayload(value)}
                className="mt-1"
              />
              <span>
                <span className="text-gray-900">{title}</span>{" "}
                <span className="text-xs text-gray-500">{help}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      {error != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(error as Error).message}
        </p>
      )}

      <div className="flex gap-2">
        <button
          type="button"
          onClick={() => {
            onCreate({ label: label.trim(), url: url.trim(), payload });
            reset();
          }}
          disabled={pending || !ready}
          className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {pending ? "Adding…" : "Add"}
        </button>
        <button
          type="button"
          onClick={() => {
            reset();
            setOpen(false);
          }}
          className="rounded-sm border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

function CreateSubscriptionForm({
  destinations,
  events,
  onCreate,
  pending,
  error,
}: {
  destinations: Webhook[];
  events: string[];
  onCreate: (body: {
    endpoint_id: string;
    scope: "job" | "batch" | "owner";
    cluster_id?: number;
    proc_id?: number;
    batch_id?: string;
    batch_name?: string;
    events: string[];
  }) => void;
  pending: boolean;
  error: unknown;
}) {
  const [open, setOpen] = useState(false);
  const [endpoint, setEndpoint] = useState("");
  const [scope, setScope] = useState<"job" | "batch" | "owner">("owner");
  const [job, setJob] = useState("");
  const [batch, setBatch] = useState("");
  const [chosen, setChosen] = useState<string[]>(["failed"]);

  const toggle = (e: string) =>
    setChosen((cur) =>
      cur.includes(e) ? cur.filter((x) => x !== e) : [...cur, e],
    );

  // "42.1" or "42", which is how a job is written everywhere else in the UI.
  const parsedJob = (() => {
    const m = /^(\d+)(?:\.(\d+))?$/.exec(job.trim());
    if (!m) return null;
    return { cluster: Number(m[1]), proc: m[2] ? Number(m[2]) : 0 };
  })();

  // Default to the only destination there is, which is the common case and
  // saves a pointless choice.
  const chosenEndpoint =
    endpoint || (destinations.length === 1 ? destinations[0].id : "");

  const ready =
    chosenEndpoint !== "" &&
    chosen.length > 0 &&
    (scope !== "job" || parsedJob !== null) &&
    (scope !== "batch" || batch.trim() !== "");

  // Reset everything, including the scope and the events. Leaving them set
  // is how somebody adds a second subscription still carrying the last
  // one's job id without noticing.
  const reset = () => {
    setEndpoint("");
    setScope("owner");
    setJob("");
    setBatch("");
    setChosen(["failed"]);
  };

  const submit = () => {
    const body = {
      endpoint_id: chosenEndpoint,
      scope,
      events: chosen,
    } as Parameters<typeof onCreate>[0];
    if (scope === "job" && parsedJob) {
      body.cluster_id = parsedJob.cluster;
      body.proc_id = parsedJob.proc;
    }
    if (scope === "batch") {
      // A batch is either a name or a bare cluster number, the way the jobs
      // page labels one. A number with no name is a lone cluster.
      const trimmed = batch.trim();
      if (/^\d+$/.test(trimmed)) body.batch_id = trimmed;
      else body.batch_name = trimmed;
    }
    onCreate(body);
    reset();
  };

  if (destinations.length === 0) return null;

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700"
      >
        Notify me about something
      </button>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border border-gray-200 bg-gray-50 p-4">
      <label className="block">
        <span className="text-xs font-medium text-gray-700">Send it to</span>
        <select
          value={chosenEndpoint}
          onChange={(e) => setEndpoint(e.target.value)}
          className="mt-1 w-full rounded-sm border border-gray-300 bg-white px-2 py-1 text-sm text-gray-900 sm:w-72"
        >
          {destinations.length !== 1 && <option value="">Choose one…</option>}
          {destinations.map((d) => (
            <option key={d.id} value={d.id}>
              {d.label}
              {d.destination_hint ? ` — ${d.destination_hint}` : ""}
            </option>
          ))}
        </select>
      </label>

      <fieldset>
        <legend className="text-xs font-medium text-gray-700">
          Notify me about
        </legend>
        <div className="mt-1 flex flex-wrap gap-3">
          {(
            [
              ["owner", "all my jobs"],
              ["batch", "one batch"],
              ["job", "one job"],
            ] as const
          ).map(([value, text]) => (
            <label key={value} className="flex items-center gap-1.5 text-sm">
              <input
                type="radio"
                name="scope"
                checked={scope === value}
                onChange={() => setScope(value)}
              />
              <span className="text-gray-900">{text}</span>
            </label>
          ))}
        </div>
      </fieldset>

      {scope === "job" && (
        <label className="block">
          <span className="text-xs font-medium text-gray-700">Job</span>
          <input
            type="text"
            value={job}
            onChange={(e) => setJob(e.target.value)}
            placeholder="1234.0"
            className="mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm text-gray-900 sm:w-48"
          />
          {job.trim() !== "" && !parsedJob && (
            <span className="mt-1 block text-xs text-amber-700">
              Write a job as cluster.proc, for example 1234.0.
            </span>
          )}
        </label>
      )}
      {scope === "batch" && (
        <label className="block">
          <span className="text-xs font-medium text-gray-700">Batch</span>
          <input
            type="text"
            value={batch}
            onChange={(e) => setBatch(e.target.value)}
            placeholder="nightly, or a cluster number"
            className="mt-1 w-full rounded-sm border border-gray-300 px-2 py-1 text-sm text-gray-900 sm:w-72"
          />
          <span className="mt-1 block text-xs text-gray-500">
            The batch name from the jobs page, or a cluster number for a batch
            with no name.
          </span>
        </label>
      )}

      {scope === "owner" && (
        <p className="rounded-sm bg-amber-50 px-2 py-1 text-xs text-amber-800">
          Watching everything also covers jobs that are already in that state
          — a notification goes out for each of them as soon as you add this.
        </p>
      )}

      <fieldset>
        <legend className="text-xs font-medium text-gray-700">When</legend>
        <div className="mt-1 space-y-1">
          {events.map((e) => (
            <label key={e} className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={chosen.includes(e)}
                onChange={() => toggle(e)}
                className="mt-1"
              />
              <span>
                <span className="font-mono text-xs text-gray-900">{e}</span>{" "}
                <span className="text-xs text-gray-500">{EVENT_HELP[e]}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      {error != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(error as Error).message}
        </p>
      )}

      <div className="flex gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={pending || !ready}
          className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {pending ? "Adding…" : "Add"}
        </button>
        <button
          type="button"
          onClick={() => {
            reset();
            setOpen(false);
          }}
          className="rounded-sm border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

// NewWebhookShown is the shown-once panel for the signing secret.
//
// A receiver that verifies signatures needs this value, and nothing can
// recover it afterwards -- so the panel says what it is for as well as that
// it will not come back. It also says the one thing people get wrong: the
// value is hex and has to be decoded before it is used as the key.
function NewWebhookShown({
  created,
  onDismiss,
}: {
  created: WebhookCreated;
  onDismiss: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(created.signing_secret);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Refused on an insecure origin; the field is still selectable.
    }
  };
  return (
    <div className="space-y-2 rounded-lg border border-amber-300 bg-amber-50 p-4">
      <h3 className="text-sm font-semibold text-amber-900">
        “{created.webhook.label}” is set up
      </h3>
      <p className="text-xs text-amber-800">
        This is the signing secret. Your receiver uses it to check that a
        delivery really came from here — the signature arrives in the{" "}
        <code className="font-mono">{created.signature_header}</code> header,
        over the timestamp, the{" "}
        <code className="font-mono">{created.idempotency_header}</code> and the
        body. It is never shown again, and everything you point at this
        destination will use it, so you only have to do this once.
      </p>
      <p className="text-xs text-amber-800">
        It is hex-encoded: <strong>decode it to 32 bytes</strong> before using
        it as the key. Using the text as-is still produces a valid-looking
        signature that never matches, with nothing to say why.
      </p>
      <div className="flex gap-2">
        <input
          type="text"
          readOnly
          value={created.signing_secret}
          onFocus={(e) => e.currentTarget.select()}
          className="flex-1 rounded-sm border border-amber-300 bg-white px-2 py-1 font-mono text-xs text-gray-900"
        />
        <button
          type="button"
          onClick={copy}
          className="rounded-sm border border-amber-400 bg-white px-3 py-1 text-xs font-medium text-amber-900 hover:bg-amber-100"
        >
          {copied ? "Copied!" : "Copy"}
        </button>
        <button
          type="button"
          onClick={onDismiss}
          className="rounded-sm bg-amber-600 px-3 py-1 text-xs font-medium text-white hover:bg-amber-700"
        >
          I&apos;ve saved it
        </button>
      </div>
      <p className="text-xs text-amber-800">
        If you do not need to verify signatures, you can ignore it — deliveries
        work either way.
      </p>
    </div>
  );
}

function DestinationRow({
  dest,
  feeds,
  tested,
  testing,
  onTest,
  onRemove,
  removing,
}: {
  dest: Webhook;
  feeds: number;
  tested?: WebhookTestResult;
  testing: boolean;
  onTest: () => void;
  onRemove: () => void;
  removing: boolean;
}) {
  const [showDeliveries, setShowDeliveries] = useState(false);
  // Backing off is not broken, and the row says so: the difference matters,
  // because one needs attention and the other is already recovering.
  //
  // Judged by the SERVER's own state rather than by comparing its timestamp
  // against this browser's clock. The server clears both the counter and the
  // deadline on any success, so a non-zero counter is its statement that the
  // destination is failing now -- and a clock a few minutes out would
  // otherwise make a healthy destination look stuck, or a stuck one look
  // fine.
  const backoff = dest.backoff_until ? new Date(dest.backoff_until) : null;
  const waiting = backoff != null && dest.consecutive_failures > 0;

  return (
    <div
      className={`rounded-lg border p-3 ${
        waiting ? "border-amber-300 bg-amber-50" : "border-gray-200"
      }`}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-gray-900">{dest.label}</span>
            <span className="text-xs text-gray-500">
              {feeds === 0
                ? "nothing is sent here yet"
                : `${feeds} ${feeds === 1 ? "subscription" : "subscriptions"}`}
            </span>
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {dest.destination_hint || "destination stored"}
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {dest.payload === "full"
              ? "Sends the whole job ad"
              : "Sends a summary of the job"}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={onTest}
            disabled={testing}
            className="rounded-sm border border-gray-300 bg-white px-3 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
          >
            {testing ? "Testing…" : "Test"}
          </button>
          <ConfirmButton
            label="Delete"
            title={
              feeds > 0
                ? `Delete this destination, the ${feeds} ${
                    feeds === 1 ? "subscription" : "subscriptions"
                  } sending to it, and everything queued`
                : "Delete this destination and everything queued for it"
            }
            compact
            pending={removing}
            onConfirm={onRemove}
          />
        </div>
      </div>

      {/* The outcome of the button the user just pressed, which is a real
          delivery and so the only honest answer about the configuration. */}
      {tested && (
        <p
          className={`mt-2 rounded-sm px-2 py-1 text-xs ${
            tested.ok ? "bg-green-50 text-green-800" : "bg-red-50 text-red-700"
          }`}
        >
          {tested.ok
            ? `Delivered — your receiver answered ${tested.status}.`
            : `Not delivered${tested.status ? ` (${tested.status})` : ""}: ${tested.error}`}
        </p>
      )}

      {waiting && backoff && (
        <p className="mt-2 rounded-sm bg-amber-100 px-2 py-1 text-xs text-amber-900">
          Your receiver is not answering, so deliveries are waiting rather
          than being thrown away. Next try at{" "}
          {backoff.toLocaleTimeString()}
          {dest.consecutive_failures > 1 &&
            ` (${dest.consecutive_failures} failures in a row)`}
          . Nothing is lost: when it answers again, the queue drains.
        </p>
      )}

      {dest.backlog_at && (
        <p className="mt-2 rounded-sm bg-red-50 px-2 py-1 text-xs text-red-800">
          Too much has queued up for this destination ({dest.backlog_depth}{" "}
          waiting), so some events have been dropped. Fix the receiver, or
          narrow what you send here.
        </p>
      )}

      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500">
        <span>
          Last delivery:{" "}
          {dest.last_delivery_at
            ? `${new Date(dest.last_delivery_at).toLocaleString()}${
                dest.last_status ? ` (${dest.last_status})` : ""
              }`
            : "never"}
        </span>
        {dest.last_test_at && (
          <span>
            Last test: {new Date(dest.last_test_at).toLocaleString()}
            {dest.last_test_status ? ` (${dest.last_test_status})` : ""}
          </span>
        )}
        <button
          type="button"
          onClick={() => setShowDeliveries((v) => !v)}
          className="text-blue-600 hover:underline"
        >
          {showDeliveries ? "Hide attempts" : "Recent attempts"}
        </button>
      </div>

      {dest.last_error && !waiting && (
        <p className="mt-1 text-xs text-gray-500">{dest.last_error}</p>
      )}

      {showDeliveries && <Deliveries id={dest.id} />}
    </div>
  );
}

function SubscriptionRow({
  sub,
  dest,
  onRemove,
  removing,
}: {
  sub: WebhookSubscription;
  dest?: Webhook;
  onRemove: () => void;
  removing: boolean;
}) {
  return (
    <div className="rounded-lg border border-gray-200 p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-gray-900">{subjectOf(sub)}</span>
            <span className="text-xs text-gray-500">
              → {dest?.label ?? "a destination that has gone"}
            </span>
          </div>
          <div className="mt-1">
            <ChipList items={sub.events} max={5} />
          </div>
        </div>
        <ConfirmButton
          label="Stop"
          title="Stop watching this. The destination and its signing secret stay."
          compact
          pending={removing}
          onConfirm={onRemove}
        />
      </div>

      {sub.capped_at && (
        <p className="mt-2 rounded-sm bg-amber-100 px-2 py-1 text-xs text-amber-900">
          {sub.capped_jobs} jobs matched at once, which is more than this
          server will post about individually. A summary was sent instead, and
          per-job notifications resume on their own if the number drops.
        </p>
      )}

      {sub.expires_at && (
        <p className="mt-2 text-xs text-gray-500">
          Expires {new Date(sub.expires_at).toLocaleString()}
        </p>
      )}
    </div>
  );
}

// Deliveries is the per-destination history, loaded only when asked for:
// most rows are never expanded, and a list per destination on every page
// load would be a query per destination for something nobody looked at.
function Deliveries({ id }: { id: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["webhooks", id, "deliveries"],
    queryFn: () => api.webhooks.deliveries(id),
  });
  if (isLoading) return <p className="mt-2 text-xs text-gray-500">Loading…</p>;
  if (error)
    return (
      <p className="mt-2 text-xs text-red-700">{(error as Error).message}</p>
    );
  const rows = data?.deliveries ?? [];
  if (rows.length === 0)
    return <p className="mt-2 text-xs text-gray-500">Nothing has been sent yet.</p>;
  return (
    <table className="mt-2 w-full text-xs">
      <thead className="text-left text-gray-500">
        <tr>
          <th className="py-1 pr-3 font-medium">Event</th>
          <th className="py-1 pr-3 font-medium">Job</th>
          <th className="py-1 pr-3 font-medium">Outcome</th>
          <th className="py-1 font-medium">Tries</th>
        </tr>
      </thead>
      <tbody className="text-gray-700">
        {rows.map((d) => (
          <tr key={d.id} className="border-t border-gray-100">
            <td className="py-1 pr-3 font-mono">{d.event}</td>
            <td className="py-1 pr-3 font-mono">{d.job ?? "—"}</td>
            <td className="py-1 pr-3">
              {/* "abandoned" is deliberately not "failed": we stopped
                  trying, which is a different thing to report. */}
              {d.state === "delivered" && `delivered (${d.status})`}
              {d.state === "pending" && "waiting to retry"}
              {d.state === "abandoned" && `given up: ${d.error}`}
            </td>
            <td className="py-1">{d.attempts}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// subjectOf renders what a subscription watches.
function subjectOf(s: WebhookSubscription): string {
  switch (s.scope) {
    case "job":
      return s.job ? `job ${s.job.cluster_id}.${s.job.proc_id}` : "one job";
    case "batch":
      return `batch ${s.batch_name || s.batch_id}`;
    default:
      return "all my jobs";
  }
}
