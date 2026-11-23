import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { SSHCAInfo, SSHCertificateRecord } from '@/lib/api';

// SSH certificates on the Connectors page.
//
// The two behaviours worth holding: a long lifetime is OFFERED -- the
// point of recording certificates was to make a week-long one safe to
// issue -- and only an outstanding certificate shows a Revoke button, so
// nobody presses one in an emergency and believes it did something.

const ca = vi.fn();
const certificates = vi.fn();
const certificate = vi.fn();
const revokeCertificate = vi.fn();

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      ssh: {
        ca: () => ca(),
        certificates: () => certificates(),
        certificate: (k: string, l?: number) => certificate(k, l),
        revokeCertificate: (s: string) => revokeCertificate(s),
      },
    },
  };
});

const caInfo: SSHCAInfo = {
  public_key: 'ssh-ed25519 AAAAC3CA',
  known_hosts_line: '@cert-authority ap.example.edu ssh-ed25519 AAAAC3CA',
  fingerprint: 'SHA256:cafe',
  gateway_host: 'ap.example.edu',
};

function cert(over: Partial<SSHCertificateRecord> = {}): SSHCertificateRecord {
  const hour = 60 * 60 * 1000;
  return {
    serial: '12345678901234567',
    key_fingerprint: 'SHA256:laptopkey',
    key_type: 'ssh-ed25519',
    comment: 'bbockelm@laptop',
    issued_at: new Date(Date.now() - hour).toISOString(),
    valid_before: new Date(Date.now() + 11 * hour).toISOString(),
    ...over,
  };
}

// Rendered beside a sentinel.
//
// "Renders nothing" and "threw during render" produce the SAME empty
// container: React unmounts the tree when a render throws and there is
// no boundary. The sentinel tells them apart -- it is still on the page
// after a graceful return null, and gone after a crash -- which is the
// difference between a section that is not part of this deployment and
// one that takes the rest of the Connectors page down with it.
async function renderSection() {
  const { SSHSection } = await import('./SSHSection');
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SSHSection />
      <span data-testid="sentinel">rest of the page</span>
    </QueryClientProvider>,
  );
}

// The section's own output, with the sentinel discounted.
function sectionText(container: HTMLElement): string {
  return (container.textContent ?? '').replace('rest of the page', '');
}

describe('the SSH section', () => {
  beforeEach(() => {
    ca.mockReset().mockResolvedValue(caInfo);
    certificates.mockReset().mockResolvedValue([]);
    certificate.mockReset();
    revokeCertificate.mockReset().mockResolvedValue({ revoked: true });
  });

  // A deployment with no CA has nothing to offer. The endpoint answers
  // 503, which is a statement about the installation and not a fault, so
  // the section disappears rather than showing a red box.
  it('renders nothing when the access point issues no certificates', async () => {
    ca.mockRejectedValue(new Error('503'));
    const { container } = await renderSection();
    await waitFor(() => expect(ca).toHaveBeenCalled());
    await waitFor(() => expect(sectionText(container)).toBe(''));
    // And it got there by returning nothing, not by throwing.
    expect(screen.getByTestId('sentinel')).toBeInTheDocument();
  });

  // The reason certificates are recorded at all: a week-long one is
  // reasonable to issue once it can be withdrawn. If this option
  // disappears the feature has quietly reverted to the old cap.
  it('offers a week for unattended work', async () => {
    await renderSection();
    const select = await screen.findByTestId('ssh-lifetime');
    const options = Array.from(select.querySelectorAll('option')).map(
      (o) => o.textContent ?? '',
    );
    expect(options.some((o) => o.includes('7 days'))).toBe(true);
    // And the default is still a working day, because most people here
    // are a person at a laptop.
    expect((select as HTMLSelectElement).value).toBe('12');
  });

  it('sends the chosen lifetime in seconds', async () => {
    certificate.mockResolvedValue({
      certificate: 'ssh-ed25519-cert-v01@openssh.com AAAA',
      principal: 'bbockelm',
      valid_before: new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString(),
      fingerprint: 'SHA256:laptopkey',
      serial: '99',
    });
    await renderSection();
    fireEvent.change(await screen.findByTestId('ssh-public-key'), {
      target: { value: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 you@laptop' },
    });
    fireEvent.change(screen.getByTestId('ssh-lifetime'), { target: { value: '168' } });
    fireEvent.click(screen.getByRole('button', { name: /issue certificate/i }));
    await waitFor(() =>
      expect(certificate).toHaveBeenCalledWith(
        'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 you@laptop',
        168 * 3600,
      ),
    );
  });

  it('lists an outstanding certificate by its comment and fingerprint', async () => {
    certificates.mockResolvedValue([cert()]);
    await renderSection();
    expect(await screen.findByText('bbockelm@laptop')).toBeInTheDocument();
    expect(screen.getByText('SHA256:laptopkey')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
  });

  // The serial is 63 bits and JavaScript carries 53. It must travel as
  // the string the server sent -- a Number() anywhere on this path
  // rounds it, and the rounded value revokes nothing.
  it('revokes using the serial exactly as the server spelled it', async () => {
    certificates.mockResolvedValue([cert({ serial: '9007199254740993' })]);
    await renderSection();
    // ConfirmButton asks first; the first click is the ask.
    fireEvent.click(await screen.findByText('Revoke'));
    fireEvent.click(screen.getByText('Confirm'));
    await waitFor(() =>
      expect(revokeCertificate).toHaveBeenCalledWith('9007199254740993'),
    );
  });

  // An expired certificate cannot be withdrawn -- there is nothing to
  // withdraw. Offering the button would invite somebody to press it
  // during an incident and believe the machine was cut off.
  it('offers no Revoke on an expired certificate', async () => {
    certificates.mockResolvedValue([
      cert({ valid_before: new Date(Date.now() - 1000).toISOString() }),
    ]);
    await renderSection();
    expect(await screen.findByText('Expired')).toBeInTheDocument();
    expect(screen.queryByText('Revoke')).toBeNull();
  });

  // Revoked and Expired are not the same report: one was somebody's
  // decision and the other was the clock.
  it('says revoked rather than expired for one the user withdrew', async () => {
    certificates.mockResolvedValue([cert({ revoked_at: new Date().toISOString() })]);
    await renderSection();
    expect(await screen.findByText('Revoked')).toBeInTheDocument();
    expect(screen.queryByText('Expired')).toBeNull();
    expect(screen.queryByText('Revoke')).toBeNull();
  });

  // An older server serves the CA but not the listing. A missing
  // feature must not be reported as a fault, and the issuing form above
  // it has to keep working.
  it('keeps the form when the listing endpoint is not served', async () => {
    certificates.mockRejectedValue(new Error('404'));
    await renderSection();
    // Wait for the listing to have FAILED before asserting, or this
    // passes on the render that happened before the rejection landed.
    await waitFor(() => expect(certificates).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull());
    expect(screen.getByTestId('ssh-public-key')).toBeInTheDocument();
    expect(screen.getByTestId('sentinel')).toBeInTheDocument();
    expect(screen.queryByText(/No certificates have been issued/)).toBeNull();
  });
});
