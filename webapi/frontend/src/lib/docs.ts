// Rendering helpers for the repository's own documentation tree.
//
// The content is docs/ at the repository root. Its structure, titles and
// ordering come from docs/nav.json, which `make docs-nav` generates from
// the tree and `make docs-check` keeps honest. Nothing here re-derives
// what that manifest already states: the page list, each page's URL and
// its title are read from it, so a page cannot appear in the app under a
// URL the manifest does not know about.
//
// What this file does add is the two mappings the manifest cannot carry,
// because on disk they are file paths and in a browser they are URLs:
// a relative link between two pages, and a heading anchor.

/** One page of the documentation tree, as docs/nav.json describes it. */
export interface DocPage {
  /** Slash-separated, relative to the docs root, e.g. "operator/auth.md". */
  path: string;
  /** Where the app serves it, e.g. "/docs/operator/auth". */
  url: string;
  title: string;
  audience: string;
  order: number;
  since?: string;
  description?: string;
}

/** One audience's pages, in reading order. */
export interface DocGuide {
  audience: string;
  title: string;
  pages: DocPage[];
}

/** docs/nav.json. */
export interface DocNav {
  guides: DocGuide[];
}

/** The URL prefix every documentation page is served under. */
export const DOCS_PREFIX = '/docs';

// Links that leave the documentation tree -- into design_notes/, into a
// package README, into examples/ -- resolve on disk, which is why
// `docs-check` accepts them, but there is no page in this app to send
// them to. They go to the repository instead.
//
// They point at the default branch rather than at the running version:
// the targets are development material that is not part of a release, so
// a tag-pinned URL into a published tree would fail as often as it
// worked.
const REPO = 'https://github.com/htcondor/golang-htcondor';
export const REPO_BLOB = `${REPO}/blob/main`;
export const REPO_TREE = `${REPO}/tree/main`;

/** Every page in the manifest, guides flattened, in navigation order. */
export function navPages(nav: DocNav): DocPage[] {
  return nav.guides.flatMap((g) => g.pages);
}

/** The path-to-URL map the link rewriter resolves against. */
export function urlByPath(nav: DocNav): Map<string, string> {
  return new Map(navPages(nav).map((p) => [p.path, p.url]));
}

/**
 * The catch-all route parameter for a page URL: "/docs/user/jobs" becomes
 * ["user", "jobs"]. Throws on a URL outside the documentation prefix,
 * which would mean the manifest and this route disagree about where the
 * tree is served.
 */
export function slugForUrl(url: string): string[] {
  if (url !== DOCS_PREFIX && !url.startsWith(`${DOCS_PREFIX}/`)) {
    throw new Error(`docs: ${url} is not under ${DOCS_PREFIX}`);
  }
  return url.slice(DOCS_PREFIX.length).split('/').filter(Boolean);
}

const LINK_TEXT = /\[([^\]]*)\]\([^)]*\)/g;

/**
 * The fragment identifier a heading gets, slugged the way GitHub slugs
 * one -- and, more to the point, the way `internal/docsmeta`.Slug does,
 * since that is what validates every anchor link in the tree. Link
 * syntax and inline code reduce to their text; letters, digits, hyphen
 * and underscore survive; a space becomes a hyphen; everything else is
 * dropped.
 */
export function slugify(heading: string): string {
  const text = heading
    .replace(LINK_TEXT, '$1')
    .replace(/`/g, '')
    .replace(/\*/g, '')
    .toLowerCase();
  let out = '';
  for (const ch of text) {
    if (/[a-z0-9\-_]/.test(ch)) out += ch;
    else if (ch === ' ') out += '-';
  }
  return out;
}

/**
 * Assigns heading ids in document order, disambiguating a repeated
 * heading with the -1, -2 suffixes GitHub uses and docsmeta reproduces.
 * One instance per rendered page.
 */
export class HeadingIds {
  private seen = new Map<string, number>();

  next(heading: string): string {
    const slug = slugify(heading);
    if (!slug) return '';
    const n = this.seen.get(slug) ?? 0;
    this.seen.set(slug, n + 1);
    return n > 0 ? `${slug}-${n}` : slug;
  }
}

/** Where a relative link resolves to, once it has left the docs root. */
export interface ResolvedTarget {
  /** "page" when it is inside this tree, "repo" when it is not. */
  kind: 'page' | 'repo';
  /** Docs-root-relative for a page, repository-root-relative for a repo link. */
  path: string;
}

/**
 * Resolves a relative link target against the page that contains it.
 * Both paths are slash-separated; `from` is docs-root-relative, as the
 * manifest spells it.
 *
 * A target that climbs above the docs root is a link into the rest of
 * the repository and comes back as kind "repo"; one that climbs above
 * the repository root is a mistake and throws.
 */
export function resolveTarget(from: string, target: string): ResolvedTarget {
  const dir = from.split('/').slice(0, -1);
  const out: string[] = [];
  let up = 0;
  for (const seg of [...dir, ...target.split('/')]) {
    if (seg === '' || seg === '.') continue;
    if (seg !== '..') {
      out.push(seg);
      continue;
    }
    if (out.length > 0) out.pop();
    else up++;
  }
  const trailing = target.endsWith('/') ? '/' : '';
  if (up === 0) return { kind: 'page', path: out.join('/') + trailing };
  if (up === 1) return { kind: 'repo', path: out.join('/') + trailing };
  throw new Error(`docs: ${from}: link to ${target} climbs above the repository`);
}

/**
 * Turns a link as written in a Markdown page into a href that works in
 * the browser.
 *
 * Anything with a scheme, a protocol-relative host, a site-absolute path
 * or a bare fragment is left alone. A relative link to another page of
 * the tree becomes that page's URL from the manifest. A relative link
 * out of the tree becomes a repository link.
 *
 * It throws on a link that resolves inside docs/ to something the
 * manifest does not list. That case is the one worth failing a build
 * over: on disk the file is there, so `docs-check` passes, but in the
 * app there is no page to serve and the reader gets the dashboard.
 */
export function rewriteHref(href: string, from: string, urls: Map<string, string>): string {
  if (href === '') return href;
  if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith('//') || href.startsWith('/')) {
    return href;
  }
  if (href.startsWith('#')) return href;

  const hash = href.indexOf('#');
  const target = hash < 0 ? href : href.slice(0, hash);
  const fragment = hash < 0 ? '' : href.slice(hash);
  if (target === '') return href;

  const resolved = resolveTarget(from, target);
  if (resolved.kind === 'repo') {
    const base = resolved.path.endsWith('/') ? REPO_TREE : REPO_BLOB;
    return `${base}/${resolved.path.replace(/\/$/, '')}${fragment}`;
  }
  const url = urls.get(resolved.path);
  if (!url) {
    throw new Error(
      `docs: ${from}: link to ${target} resolves to ${resolved.path}, ` +
        `which docs/nav.json does not list as a page -- it would 404 in the app`,
    );
  }
  return `${url}${fragment}`;
}
