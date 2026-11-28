// Reading the documentation tree off disk at build time.
//
// Server-side only: this module uses node:fs and is imported by the
// documentation route's server components and by its tests, never by a
// client component.
//
// The tree is not inside this app. It is docs/ at the repository root,
// shared with `make docs-check`, with the Markdown the package installs,
// and with whatever renders it next. DOCS_ROOT overrides where to find
// it, which is how the container build points at the tree it copied into
// its frontend stage; the default is right for a checkout.
//
// Nothing here parses front matter for its meaning. docs/nav.json
// carries every field of it that the app uses, so this module only has
// to remove the block before the Markdown is rendered.

import fs from 'node:fs';
import path from 'node:path';

import type { DocNav, DocPage } from './docs';
import { navPages } from './docs';

/**
 * The documentation tree's directory. Throws when it is not there,
 * rather than exporting a site with no documentation in it: an empty
 * `generateStaticParams` is a silent 404 on every page, and the message
 * a missing tree deserves names the path it looked in.
 */
export function docsRoot(): string {
  const root = process.env.DOCS_ROOT
    ? path.resolve(process.env.DOCS_ROOT)
    : path.resolve(process.cwd(), '..', '..', 'docs');
  if (!fs.existsSync(path.join(root, 'nav.json'))) {
    throw new Error(
      `docs: no nav.json under ${root}. The documentation tree is docs/ at the ` +
        `repository root; set DOCS_ROOT when it is somewhere else, and run ` +
        `\`make docs-nav\` if the manifest is missing.`,
    );
  }
  return root;
}

let cached: DocNav | undefined;

/** docs/nav.json, read once per process. */
export function loadNav(): DocNav {
  if (!cached) {
    cached = JSON.parse(fs.readFileSync(path.join(docsRoot(), 'nav.json'), 'utf8')) as DocNav;
  }
  return cached;
}

/** Every page of the tree, guides flattened, in navigation order. */
export function loadPages(): DocPage[] {
  return navPages(loadNav());
}

/**
 * One page's Markdown, with the front matter block removed. The block is
 * delimited exactly as `internal/docsmeta` requires it -- the file opens
 * with `---` and the block ends at the next `---` on its own line -- so a
 * file that reaches here has already been validated by `make docs-check`.
 */
export function loadBody(docPath: string): string {
  const text = fs.readFileSync(path.join(docsRoot(), docPath), 'utf8');
  if (!text.startsWith('---\n')) return text;
  const end = text.indexOf('\n---\n', '---\n'.length);
  if (end < 0) return text;
  return text.slice(end + '\n---\n'.length);
}
