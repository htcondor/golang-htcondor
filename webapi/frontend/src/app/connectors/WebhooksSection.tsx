"use client";

// Event notifications: the outbound half.
//
// This server posts to a service when the user's jobs change state, which is
// the opposite direction to the apps section above it -- an app connected
// there acts as the user HERE, a webhook tells a service what happened here.
// Both headings say which way, because revoking the wrong one is the mistake
// the page exists to prevent.
//
// Two things on this panel are load-bearing rather than decorative. The
// signing secret is shown once and never again, so the moment it appears is
// the moment the user has to copy it. And Test sends a REAL delivery, so its
// result is the only honest answer to "is this configured right".

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type Webhook,
  type WebhookCreated,
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

  const { data, isLoading, error } = useQuery({
    queryKey: ["webhooks"],
    queryFn: api.webhooks.list,
  });

  const create = useMutation({
    mutationFn: api.webhooks.create,
    onSuccess: (res) => {
      setCreated(res);
      qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.webhooks.remove(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["webhooks"] }),
  });

  const enable = useMutation({
    mutationFn: (id: string) => api.webhooks.enable(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["webhooks"] }),
  });

  const test = useMutation({
    mutationFn: (id: string) => api.webhooks.test(id),
    onSuccess: (res, id) => {
      setTested((cur) => ({ ...cur, [id]: res }));
      // The attempt is recorded against the hook, so the row's own "last
      // test" line comes from the server rather than from this state.
      qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });

  const hooks = data?.webhooks ?? [];
  const events = data?.events ?? [];

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold text-gray-900">
          Event notifications
        </h2>
        <p className="mt-1 text-sm text-gray-500">
          This access point posts to a URL you give it when your jobs change
          state — for a Zap, a Slack channel, or anything that accepts a
          webhook. The URL is treated as a secret, because for most services
          holding it is all it takes to post there.
        </p>
      </div>

      {created && (
        <NewWebhookShown created={created} onDismiss={() => setCreated(null)} />
      )}

      <CreateWebhookForm
        events={events}
        pending={create.isPending}
        error={create.error}
        onCreate={(body) => create.mutate(body)}
      />

      {isLoading && <p className="text-sm text-gray-500">Loading…</p>}
      {error && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(error as Error).message}
        </p>
      )}

      {!isLoading && !error && hooks.length === 0 && (
        <p className="rounded-lg border border-dashed border-gray-300 px-4 py-6 text-center text-sm text-gray-500">
          No event notifications are set up.
        </p>
      )}

      <div className="space-y-3">
        {hooks.map((h) => (
          <WebhookRow
            key={h.id}
            hook={h}
            tested={tested[h.id]}
            testing={test.isPending && test.variables === h.id}
            onTest={() => test.mutate(h.id)}
            onEnable={() => enable.mutate(h.id)}
            onRemove={() => remove.mutate(h.id)}
            removing={remove.isPending}
          />
        ))}
      </div>
    </section>
  );
}

function CreateWebhookForm({
  events,
  onCreate,
  pending,
  error,
}: {
  events: string[];
  onCreate: (body: {
    label: string;
    url: string;
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
  const [label, setLabel] = useState("");
  const [url, setUrl] = useState("");
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

  const ready =
    label.trim() !== "" &&
    url.trim() !== "" &&
    chosen.length > 0 &&
    (scope !== "job" || parsedJob !== null) &&
    (scope !== "batch" || batch.trim() !== "");

  const submit = () => {
    const body = {
      label: label.trim(),
      url: url.trim(),
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
    setLabel("");
    setUrl("");
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700"
      >
        Add a notification
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
          onClick={() => setOpen(false)}
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
// it will not come back.
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
        body. It is never shown again.
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

function WebhookRow({
  hook,
  tested,
  testing,
  onTest,
  onEnable,
  onRemove,
  removing,
}: {
  hook: Webhook;
  tested?: WebhookTestResult;
  testing: boolean;
  onTest: () => void;
  onEnable: () => void;
  onRemove: () => void;
  removing: boolean;
}) {
  const [showDeliveries, setShowDeliveries] = useState(false);
  const disabled = hook.disabled_at != null;

  return (
    <div
      className={`rounded-lg border p-3 ${
        disabled ? "border-amber-300 bg-amber-50" : "border-gray-200"
      }`}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-gray-900">{hook.label}</span>
            <span className="text-xs text-gray-500">{subjectOf(hook)}</span>
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {hook.destination_hint || "destination stored"}
          </div>
          <div className="mt-1">
            <ChipList items={hook.events} max={5} />
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
          {disabled && (
            <button
              type="button"
              onClick={onEnable}
              className="rounded-sm border border-amber-400 bg-white px-3 py-1 text-xs font-medium text-amber-900 hover:bg-amber-100"
            >
              Re-enable
            </button>
          )}
          <ConfirmButton
            label="Delete"
            title="Delete this notification and everything queued for it"
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
            tested.ok
              ? "bg-green-50 text-green-800"
              : "bg-red-50 text-red-700"
          }`}
        >
          {tested.ok
            ? `Delivered — your receiver answered ${tested.status}.`
            : `Not delivered${tested.status ? ` (${tested.status})` : ""}: ${tested.error}`}
        </p>
      )}

      {disabled && (
        <p className="mt-2 rounded-sm bg-amber-100 px-2 py-1 text-xs text-amber-900">
          {hook.disabled_reason || "This notification has been turned off."}
        </p>
      )}

      {hook.capped_at && (
        <p className="mt-2 rounded-sm bg-amber-100 px-2 py-1 text-xs text-amber-900">
          {hook.capped_jobs} jobs matched at once, which is more than this
          server will post about individually. A summary was sent instead.
        </p>
      )}

      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500">
        <span>
          Last delivery:{" "}
          {hook.last_delivery_at
            ? `${new Date(hook.last_delivery_at).toLocaleString()}${
                hook.last_status ? ` (${hook.last_status})` : ""
              }`
            : "never"}
        </span>
        {hook.last_test_at && (
          <span>
            Last test: {new Date(hook.last_test_at).toLocaleString()}
            {hook.last_test_status ? ` (${hook.last_test_status})` : ""}
          </span>
        )}
        {hook.expires_at && (
          <span>Expires {new Date(hook.expires_at).toLocaleString()}</span>
        )}
        <button
          type="button"
          onClick={() => setShowDeliveries((v) => !v)}
          className="text-blue-600 hover:underline"
        >
          {showDeliveries ? "Hide attempts" : "Recent attempts"}
        </button>
      </div>

      {hook.last_error && !disabled && (
        <p className="mt-1 text-xs text-gray-500">{hook.last_error}</p>
      )}

      {showDeliveries && <Deliveries id={hook.id} />}
    </div>
  );
}

// Deliveries is the per-hook history, loaded only when asked for: most rows
// are never expanded, and a list per hook on every page load would be a query
// per hook for something nobody looked at.
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
    return (
      <p className="mt-2 text-xs text-gray-500">
        Nothing has been sent yet.
      </p>
    );
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

// subjectOf renders what a hook watches.
function subjectOf(h: Webhook): string {
  switch (h.scope) {
    case "job":
      return h.job ? `job ${h.job.cluster_id}.${h.job.proc_id}` : "one job";
    case "batch":
      return `batch ${h.batch_name || h.batch_id}`;
    default:
      return "all my jobs";
  }
}
