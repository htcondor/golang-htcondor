import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it } from 'vitest';
import type { Branding } from '@/lib/branding';
import { Footer } from './Footer';

function renderFooter(branding?: Branding) {
  if (branding) window.__HTCONDOR_BRANDING__ = branding;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Footer />
    </QueryClientProvider>,
  );
}

const site: Branding = {
  site_name: 'UChicago ATLAS Analysis Facility',
  site_tagline: '',
  page_title: 'UChicago ATLAS Analysis Facility',
  footer_logos: [{ src: '/branding/af.svg', alt: 'ATLAS AF', href: 'https://af.example/' }],
  software_credit: true,
};

describe('Footer', () => {
  afterEach(() => {
    delete window.__HTCONDOR_BRANDING__;
  });

  it('shows the CHTC band when the site configured nothing', async () => {
    renderFooter();
    const chtc = await screen.findByAltText('Center for High Throughput Computing');
    expect(chtc).toHaveAttribute('src', '/logos/CHTC_Logo_Full_Color.svg');
    expect(screen.getByAltText('Morgridge Institute for Research')).toBeInTheDocument();
    expect(screen.getByAltText('University of Wisconsin–Madison')).toBeInTheDocument();
    // The band already names CHTC, so the credit line would say it twice.
    expect(screen.queryByText(/Powered by/)).not.toBeInTheDocument();
  });

  it("shows the site's own logos instead", async () => {
    renderFooter(site);
    await waitFor(() => expect(screen.getByAltText('ATLAS AF')).toBeInTheDocument());
    expect(screen.getByAltText('ATLAS AF')).toHaveAttribute('src', '/branding/af.svg');
    expect(screen.getByRole('link', { name: 'ATLAS AF' })).toHaveAttribute(
      'href',
      'https://af.example/',
    );
    // Nothing claims UW-Madison runs somebody else's access point.
    expect(screen.queryByAltText('University of Wisconsin–Madison')).not.toBeInTheDocument();
  });

  it('still names HTCondor and CHTC under a site band', async () => {
    renderFooter(site);
    await waitFor(() => expect(screen.getByText(/Powered by/)).toBeInTheDocument());
    expect(screen.getByRole('link', { name: 'HTCondor' })).toHaveAttribute(
      'href',
      'https://htcondor.org/',
    );
    expect(
      screen.getByRole('link', { name: 'Center for High Throughput Computing' }),
    ).toHaveAttribute('href', 'https://chtc.cs.wisc.edu/');
  });

  it('keeps the credit when a site asked for no logos at all', async () => {
    renderFooter({ ...site, footer_logos: [] });
    await waitFor(() => expect(screen.getByText(/Powered by/)).toBeInTheDocument());
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });

  // A link to a policy page the operator never wrote would 404, and a
  // reader who follows it concludes the policy is missing rather than
  // unconfigured.
  it('says nothing about policies a site has not configured', async () => {
    renderFooter(site);
    await waitFor(() => expect(screen.getByText(/Powered by/)).toBeInTheDocument());
    expect(screen.queryByRole('link', { name: 'Privacy' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Terms of Use' })).not.toBeInTheDocument();
  });

  it("links the site's policy pages when it has them", async () => {
    renderFooter({ ...site, privacy_url: '/privacy', terms_url: '/terms' });
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'Privacy' })).toBeInTheDocument(),
    );
    expect(screen.getByRole('link', { name: 'Privacy' })).toHaveAttribute('href', '/privacy');
    expect(screen.getByRole('link', { name: 'Terms of Use' })).toHaveAttribute('href', '/terms');
  });

  // A site whose privacy notice is its institution's gets a link
  // straight there, so hovering shows the reader where it really goes.
  it('links an external policy at its own address', async () => {
    const notice = 'https://www.wisc.edu/privacy-notice/';
    renderFooter({ ...site, privacy_url: notice });
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'Privacy' })).toBeInTheDocument(),
    );
    expect(screen.getByRole('link', { name: 'Privacy' })).toHaveAttribute('href', notice);
    expect(screen.queryByRole('link', { name: 'Terms of Use' })).not.toBeInTheDocument();
  });
});
