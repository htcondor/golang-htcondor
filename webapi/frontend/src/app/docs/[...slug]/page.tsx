import type { Metadata } from 'next';

import { DocsPage } from '@/components/DocsPage';
import { slugForUrl } from '@/lib/docs';
import { loadBody, loadNav, loadPages } from '@/lib/docsContent';

// The documentation route: /docs/<audience>/<page>, served from the
// repository's own docs/ tree.
//
// Static export turns a dynamic segment into real files only for the
// parameters generateStaticParams returns, so this generator is what
// decides which pages exist in the exported site. It derives them from
// docs/nav.json -- the same manifest `make docs-check` validates -- and
// from nothing else, so a page added to the tree reaches the site as
// soon as the manifest is regenerated. src/lib/docsPages.test.ts fails
// if any page in the manifest is missing from what this returns.
//
// dynamicParams is false so that a slug outside the manifest is a 404
// rather than an attempt to render a file that is not there.
export const dynamicParams = false;

interface RouteParams {
  params: Promise<{ slug: string[] }>;
}

export function generateStaticParams(): { slug: string[] }[] {
  return loadPages().map((page) => ({ slug: slugForUrl(page.url) }));
}

/** The manifest entry for a URL slug, or undefined when there is none. */
function pageForSlug(slug: string[]) {
  const url = ['/docs', ...slug].join('/');
  return loadPages().find((p) => p.url === url);
}

export async function generateMetadata({ params }: RouteParams): Promise<Metadata> {
  const page = pageForSlug((await params).slug);
  if (!page) return { title: 'Documentation' };
  return {
    title: `${page.title} — HTCondor Access Point`,
    ...(page.description ? { description: page.description } : {}),
  };
}

export default async function Page({ params }: RouteParams) {
  const { slug } = await params;
  const page = pageForSlug(slug);
  if (!page) {
    // Unreachable through the export, which only writes the pages
    // generateStaticParams named; this is the message a developer gets
    // if that ever stops being true.
    throw new Error(`docs: no page for /docs/${slug.join('/')}`);
  }
  return <DocsPage nav={loadNav()} page={page} body={loadBody(page.path)} />;
}
