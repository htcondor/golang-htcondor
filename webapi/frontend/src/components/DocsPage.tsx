import Link from 'next/link';

import type { DocNav, DocPage } from '@/lib/docs';
import { urlByPath } from '@/lib/docs';
import { DocsMarkdown } from './DocsMarkdown';

// The shell around one documentation page: the guide navigation on the
// left, the page on the right.
//
// Everything here renders at build time. The navigation is docs/nav.json
// -- the generated manifest, not a second list -- so a page that exists
// is in the navigation and a page in the navigation exists.

function GuideNav({ nav, current }: { nav: DocNav; current: string }) {
  return (
    <nav aria-label="Documentation" className="text-sm">
      {nav.guides.map((guide) => (
        <div key={guide.audience} className="mb-6">
          <h2 className="mb-2 text-xs font-semibold tracking-wide text-ink-500 uppercase">
            {guide.title}
          </h2>
          <ul className="space-y-1">
            {guide.pages.map((page) => (
              <li key={page.path}>
                <Link
                  href={page.url}
                  aria-current={page.url === current ? 'page' : undefined}
                  className={
                    page.url === current
                      ? 'block rounded px-2 py-1 bg-brand-50 font-medium text-brand-700'
                      : 'block rounded px-2 py-1 text-ink-700 hover:bg-ink-50'
                  }
                >
                  {page.title}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}

export interface DocsPageProps {
  nav: DocNav;
  page: DocPage;
  body: string;
}

export function DocsPage({ nav, page, body }: DocsPageProps) {
  const urls = urlByPath(nav);
  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-8 px-4 py-6 lg:flex-row">
      {/* <details> rather than a toggle: the navigation has to work on a
          phone and this page ships no JavaScript of its own. */}
      <details className="lg:hidden" data-testid="docs-nav-mobile">
        <summary className="cursor-pointer py-2 text-sm font-medium text-ink-700">
          All documentation
        </summary>
        <div className="pt-3">
          <GuideNav nav={nav} current={page.url} />
        </div>
      </details>
      <aside className="hidden shrink-0 lg:block lg:w-64" data-testid="docs-nav">
        <GuideNav nav={nav} current={page.url} />
      </aside>
      <article className="docs-body min-w-0 flex-1" data-testid="docs-article">
        {page.since ? (
          <p className="mb-4 text-xs text-ink-500" data-testid="docs-since">
            New in version {page.since}.
          </p>
        ) : null}
        <DocsMarkdown body={body} from={page.path} urls={urls} />
      </article>
    </div>
  );
}
