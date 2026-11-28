import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import { generateStaticParams } from '@/app/docs/[...slug]/page';
import { DocsMarkdown } from '@/components/DocsMarkdown';
import { DOCS_PREFIX, navPages, urlByPath } from '@/lib/docs';
import { loadBody, loadNav } from '@/lib/docsContent';

// These run against the real docs/ tree, not a fixture, for the same
// reason internal/docsmeta's tree tests do: the thing worth checking is
// that the tree as it stands reaches the reader.
//
// Three failures are possible here and each is silent in production:
//
//   - A page exists but the static export never writes a file for it,
//     because generateStaticParams did not name it. The exported site
//     answers that URL with the dashboard, not a 404.
//   - A link between two pages resolves on disk, so `make docs-check`
//     passes, but points at no URL this app serves.
//   - An anchor link resolves to a heading slug internal/docsmeta
//     computed and the renderer did not, so the link lands at the top
//     of the page.
//
// The pages are rendered with renderToStaticMarkup because that is what
// the static export does: a documentation page is HTML produced at
// build time, so a test that renders it the same way sees what ships.

const nav = loadNav();
const pages = navPages(nav);
const urls = urlByPath(nav);

/** Every page's rendered markup, parsed, built once. */
const rendered = new Map<string, Document>();
function renderPage(url: string): Document {
  const cached = rendered.get(url);
  if (cached) return cached;
  const page = pages.find((p) => p.url === url);
  if (!page) throw new Error(`no page for ${url}`);
  const html = renderToStaticMarkup(
    <DocsMarkdown body={loadBody(page.path)} from={page.path} urls={urls} />,
  );
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, 'text/html');
  rendered.set(url, doc);
  return doc;
}

/** Every <a href> in a page, permalinks excluded. */
function proseLinks(url: string): string[] {
  return Array.from(renderPage(url).querySelectorAll('a[href]'))
    .filter((a) => !a.classList.contains('docs-anchor'))
    .map((a) => a.getAttribute('href') ?? '');
}

describe('the documentation tree reaches the exported site', () => {
  it('has pages at all', () => {
    expect(pages.length).toBeGreaterThan(0);
  });

  // The guard the static export needs. Next writes one file per
  // parameter this returns and nothing else, so a page missing from it
  // is a page missing from the site -- with no error anywhere, because
  // the server's SPA fallback answers the URL with the dashboard.
  it('generates a static parameter for every page in the manifest', () => {
    const generated = new Set(
      generateStaticParams().map((p) => [DOCS_PREFIX, ...p.slug].join('/')),
    );
    const missing = pages.filter((p) => !generated.has(p.url));
    expect(
      missing.map((p) => `${p.path} (${p.url})`),
      'pages in docs/nav.json that generateStaticParams does not name; ' +
        'the exported site would answer their URLs with the dashboard',
    ).toEqual([]);
  });

  it('generates no parameter that is not a page', () => {
    const known = new Set(pages.map((p) => p.url));
    const extra = generateStaticParams()
      .map((p) => [DOCS_PREFIX, ...p.slug].join('/'))
      .filter((url) => !known.has(url));
    expect(extra, 'generated parameters with no page behind them').toEqual([]);
  });

  // Rendering is where rewriteHref runs, so an unresolvable link throws
  // here with the page and the target named.
  it('renders every page', () => {
    for (const page of pages) {
      expect(() => renderPage(page.url), `rendering ${page.path}`).not.toThrow();
      expect(renderPage(page.url).querySelector('h1'), `${page.path} has no H1`).not.toBeNull();
    }
  });

  it('links only to URLs the site serves', () => {
    const known = new Set(pages.map((p) => p.url));
    const broken: string[] = [];
    let checked = 0;
    for (const page of pages) {
      for (const href of proseLinks(page.url)) {
        if (!href.startsWith(`${DOCS_PREFIX}/`)) continue;
        checked++;
        if (!known.has(href.split('#')[0])) broken.push(`${page.path} -> ${href}`);
      }
    }
    expect(broken, 'links to a /docs URL with no page behind it').toEqual([]);
    // Not vacuous: the tree really does link between its pages, and a
    // rewriter that stopped producing /docs URLs would otherwise pass
    // this by checking nothing.
    expect(checked, 'cross-page links checked').toBeGreaterThan(50);
  });

  it('links only to anchors the rendered pages define', () => {
    const broken: string[] = [];
    let checked = 0;
    for (const page of pages) {
      for (const href of proseLinks(page.url)) {
        const hash = href.indexOf('#');
        if (hash < 0) continue;
        const anchor = href.slice(hash + 1);
        if (anchor === '') continue;
        const target = hash === 0 ? page.url : href.slice(0, hash);
        if (!pages.some((p) => p.url === target)) continue;
        checked++;
        if (!renderPage(target).querySelector(`[id="${CSS.escape(anchor)}"]`)) {
          broken.push(`${page.path} -> ${href}`);
        }
      }
    }
    expect(
      broken,
      'anchor links whose target heading the renderer gives a different id; ' +
        'make docs-check validated these against internal/docsmeta.Slug',
    ).toEqual([]);
    // Not vacuous, and this count is what keeps the TypeScript slugger
    // honest against the Go one: every anchor here was validated by
    // `make docs-check` against internal/docsmeta.Slug, so the two
    // agreeing on all of them is the parity check.
    expect(checked, 'anchor links checked').toBeGreaterThan(40);
  });
});
