"use client";

// SSH certificates: the credentials your OWN machines hold to reach
// this access point.
//
// This lived on /info, which was the wrong page twice over. /info is a
// readout -- version, uptime, your groups -- and this is the one thing
// on it that DID something. And what it does is the same kind of thing
// the rest of this page is about: something outside holds a credential
// that acts as you here, for a while, until you withdraw it.
//
// It is its own section rather than a row under "Apps connected to your
// account" because the holder is not an app. Nobody consented to
// anything on your behalf, there is no client and no scope screen; it
// is your laptop, and the thing that was granted is a shell. Filing it
// with the OAuth2 clients would make "Disconnect" mean two different
// operations in one list.
//
// The public key is pasted rather than generated here. A browser cannot
// read ~/.ssh/id_ed25519.pub, and generating a keypair in the page would
// mean delivering a private key through a browser -- which is worse than
// asking for one line, and a private key is the one thing this flow is
// built never to move.

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  ApiError,
  type SSHCertificate,
  type SSHCertificateRecord,
} from "@/lib/api";
import { ConfirmButton } from "@/components/ConfirmButton";

const TH =
  "px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-gray-500";

// The lifetimes offered, and the reason each one is here.
//
// A week is the cap the server enforces (sshCertMaxLifetime). It exists
// for unattended work: a job or a cron entry cannot go back to a browser
// every twelve hours, and before revocation existed the only way to run
// one was a credential with no expiry at all -- strictly worse than a
// long certificate that can be withdrawn.
//
// Twelve hours stays the default because most people here are a person
// at a laptop, and for them a working day is the right answer.
const LIFETIMES: { hours: number; label: string; hint?: string }[] = [
  { hours: 1, label: "1 hour" },
  { hours: 12, label: "12 hours", hint: "A working day" },
  { hours: 24, label: "1 day" },
  { hours: 72, label: "3 days" },
  { hours: 168, label: "7 days", hint: "For unattended jobs" },
];

export function SSHSection() {
  const qc = useQueryClient();
  const {
    data: ca,
    isLoading,
    error,
  } = useQuery({ queryKey: ["ssh", "ca"], queryFn: api.ssh.ca, retry: false });

  const [publicKey, setPublicKey] = useState("");
  const [lifetimeHours, setLifetimeHours] = useState(12);
  const [cert, setCert] = useState<SSHCertificate | null>(null);
  const [issueError, setIssueError] = useState<string | null>(null);

  const issue = useMutation({
    mutationFn: () =>
      api.ssh.certificate(publicKey.trim(), lifetimeHours * 3600),
    onMutate: () => {
      setIssueError(null);
      setCert(null);
    },
    onSuccess: (c) => {
      setCert(c);
      // The new certificate belongs in the table below, which is the
      // only place it can later be withdrawn from.
      void qc.invalidateQueries({ queryKey: ["ssh", "certificates"] });
    },
    onError: (e) =>
      setIssueError(e instanceof ApiError ? e.message : String(e)),
  });

  if (isLoading) return null;
  // 503 is "this deployment has no certificate authority", not a fault.
  // An access point with no gateway should not carry a broken-looking
  // panel about one.
  if (error || !ca) return null;

  const host = ca.gateway_host;
  const port = ca.gateway_port && ca.gateway_port !== 22 ? ca.gateway_port : 0;
  const sshCmd = host
    ? `ssh ${port ? `-p ${port} ` : ""}${host}`
    : "ssh <gateway host>";

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold text-gray-900">
          Your own machines over SSH
        </h2>
        <p className="mt-1 text-sm text-gray-500">
          A certificate lets a computer of yours reach a job over SSH without
          approving a browser prompt each time. It acts as you, until it
          expires or you withdraw it below.
        </p>
      </div>

      <div className="space-y-4 rounded-lg border border-gray-200 bg-gray-50 p-4">
        <div>
          <div className="text-sm font-medium text-gray-700">
            1. Trust the gateway
          </div>
          <p className="mt-0.5 text-xs text-gray-500">
            Append this to <code>~/.ssh/known_hosts</code>. Without it the first
            connection asks you to verify a fingerprint by hand.
          </p>
          <CopyableBlock text={ca.known_hosts_line} />
          <p className="mt-1 text-xs text-gray-500">
            CA fingerprint: <span className="font-mono">{ca.fingerprint}</span>
          </p>
        </div>

        <div>
          <div className="text-sm font-medium text-gray-700">
            2. Get a certificate
          </div>
          <p className="mt-0.5 text-xs text-gray-500">
            Paste the <strong>public</strong> half of a key you already have —
            the contents of <code>~/.ssh/id_ed25519.pub</code>. Your private key
            stays on your machine and is never sent.
          </p>
          <textarea
            value={publicKey}
            onChange={(e) => setPublicKey(e.target.value)}
            rows={3}
            spellCheck={false}
            data-testid="ssh-public-key"
            placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... you@laptop"
            className="mt-2 w-full rounded-sm border border-gray-300 bg-white px-3 py-2 font-mono text-xs"
          />
          <div className="mt-2 flex flex-wrap items-center gap-3">
            <label className="text-xs text-gray-600">
              Valid for{" "}
              <select
                value={lifetimeHours}
                data-testid="ssh-lifetime"
                onChange={(e) => setLifetimeHours(Number(e.target.value))}
                className="rounded-sm border border-gray-300 bg-white px-2 py-1 text-xs"
              >
                {LIFETIMES.map((l) => (
                  <option key={l.hours} value={l.hours}>
                    {l.label}
                    {l.hint ? ` — ${l.hint}` : ""}
                  </option>
                ))}
              </select>
            </label>
            <button
              onClick={() => issue.mutate()}
              disabled={!publicKey.trim() || issue.isPending}
              className="rounded-sm bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-60"
            >
              {issue.isPending ? "Issuing…" : "Issue certificate"}
            </button>
          </div>
          {/* Said where the choice is made rather than in the table
              below, because this is the moment somebody picks a week. */}
          <p className="mt-1 text-xs text-gray-500">
            A long certificate is for a job that runs unattended. You can
            withdraw any of them below before they expire.
          </p>
          {issueError && (
            <p className="mt-2 text-sm text-red-600">{issueError}</p>
          )}
        </div>

        {cert && (
          <div>
            <div className="text-sm font-medium text-gray-700">
              3. Save it beside your key
            </div>
            <p className="mt-0.5 text-xs text-gray-500">
              Save as <code>~/.ssh/id_ed25519-cert.pub</code> — the same name as
              your private key with <code>-cert.pub</code> appended. ssh finds
              it there on its own. Issued to{" "}
              <span className="font-mono">{cert.principal}</span>, expires{" "}
              {new Date(cert.valid_before).toLocaleString()}.
            </p>
            <CopyableBlock
              text={cert.certificate}
              download="id_ed25519-cert.pub"
            />
            <p className="mt-2 text-xs text-gray-500">
              Then connect: <code className="font-mono">{sshCmd}</code>
              {host ? null : " — ask your administrator for the gateway's name."}
            </p>
          </div>
        )}
      </div>

      <CertificateTable />
    </section>
  );
}

// CertificateTable is what makes a week-long certificate reasonable to
// hand out: every one is listed, and an outstanding one can be
// withdrawn.
function CertificateTable() {
  const qc = useQueryClient();

  // The current time, held in state rather than read during render.
  //
  // Whether a certificate is expired is the difference between offering
  // a Revoke button and not, so it cannot be a value that changes when
  // React happens to re-render. A minute is finer than anyone needs: the
  // shortest certificate on offer lasts an hour.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(t);
  }, []);
  const { data, isLoading, error } = useQuery({
    queryKey: ["ssh", "certificates"],
    queryFn: api.ssh.certificates,
    retry: false,
  });

  const revoke = useMutation({
    mutationFn: (serial: string) => api.ssh.revokeCertificate(serial),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["ssh", "certificates"] }),
  });

  // An older server serves the CA but not this listing. Showing a red
  // box there would report a missing feature as a fault.
  if (error) return null;
  if (isLoading) return <p className="text-sm text-gray-500">Loading…</p>;

  const certs = data ?? [];
  if (certs.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-gray-300 px-4 py-6 text-center text-sm text-gray-500">
        No certificates have been issued to your account.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-lg border border-gray-200">
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50">
            <tr>
              <th className={TH}>Key</th>
              <th className={TH}>Status</th>
              <th className={TH}>Issued</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 bg-white">
            {certs.map((c) => (
              <CertificateRow
                key={c.serial}
                cert={c}
                now={now}
                onRevoke={() => revoke.mutate(c.serial)}
                revoking={revoke.isPending && revoke.variables === c.serial}
              />
            ))}
          </tbody>
        </table>
      </div>
      {revoke.error != null && (
        <p className="rounded-sm border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          {(revoke.error as Error).message}
        </p>
      )}
    </div>
  );
}

function CertificateRow({
  cert,
  now,
  onRevoke,
  revoking,
}: {
  cert: SSHCertificateRecord;
  now: number;
  onRevoke: () => void;
  revoking: boolean;
}) {
  const expired = new Date(cert.valid_before).getTime() <= now;
  const revoked = Boolean(cert.revoked_at);
  // Only an outstanding certificate can be withdrawn. Offering the
  // button on an expired one would invite somebody to press it in an
  // emergency and believe it had done something.
  const outstanding = !expired && !revoked;

  return (
    <tr>
      <td className="px-3 py-2">
        {/* The comment first, because "bbockelm@laptop" is how somebody
            with three machines tells the rows apart. The fingerprint
            below it is what `ssh-keygen -lf ~/.ssh/id_ed25519.pub`
            prints on their own machine, which is how they CHECK. */}
        <div className="text-gray-900">
          {cert.comment || <span className="text-gray-500">No comment</span>}
        </div>
        <div className="font-mono text-xs text-gray-500">
          {cert.key_fingerprint}
        </div>
        <div className="text-xs text-gray-400">{cert.key_type}</div>
      </td>
      <td className="px-3 py-2">
        {/* Three states, three sentences. "Revoked" and "Expired" are
            NOT the same thing to report: one of them was somebody's
            decision and the other was the clock. */}
        {revoked ? (
          <>
            <span className="inline-flex rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
              Revoked
            </span>
            <div className="mt-1 text-xs text-gray-500">
              {new Date(cert.revoked_at as string).toLocaleString()}
            </div>
          </>
        ) : expired ? (
          <span className="inline-flex rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
            Expired
          </span>
        ) : (
          <>
            <span className="inline-flex rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
              Active
            </span>
            <div className="mt-1 text-xs text-gray-500">
              Expires {new Date(cert.valid_before).toLocaleString()}
            </div>
          </>
        )}
      </td>
      <td className="px-3 py-2 text-gray-700">
        {new Date(cert.issued_at).toLocaleString()}
      </td>
      <td className="px-3 py-2 text-right">
        {outstanding && (
          <ConfirmButton
            label="Revoke"
            title="Refuse this certificate from now on"
            compact
            pending={revoking}
            onConfirm={onRevoke}
          />
        )}
      </td>
    </tr>
  );
}

// CopyableBlock shows one long line that a person has to get into a file
// exactly, with both ways of doing that: the clipboard, and a download
// for when the clipboard is not available or the line is long enough
// that a failed paste would be silent.
function CopyableBlock({
  text,
  download,
}: {
  text: string;
  download?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="mt-2">
      <pre className="overflow-x-auto rounded-sm border border-gray-200 bg-white p-2 font-mono text-xs break-all whitespace-pre-wrap">
        {text}
      </pre>
      <div className="mt-1 flex gap-3">
        <button
          onClick={() => {
            navigator.clipboard
              .writeText(text)
              .then(() => {
                setCopied(true);
                setTimeout(() => setCopied(false), 2000);
              })
              .catch(() => setCopied(false));
          }}
          className="text-xs text-blue-700 underline hover:text-blue-900"
        >
          {copied ? "Copied" : "Copy"}
        </button>
        {download && (
          <a
            href={`data:application/octet-stream;charset=utf-8,${encodeURIComponent(text + "\n")}`}
            download={download}
            className="text-xs text-blue-700 underline hover:text-blue-900"
          >
            Download
          </a>
        )}
      </div>
    </div>
  );
}
