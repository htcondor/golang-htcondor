import ReactMarkdown, { type Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';

import { HeadingIds, rewriteHref } from '@/lib/docs';

// The Markdown renderer for a documentation page.
//
// A server component: every documentation page is prerendered at build
// time, so what the browser receives is HTML. That is deliberate and
// costs nothing here, because the content is fixed when the binary is
// built. It means the pages read with JavaScript disabled, they cost no
// request beyond the page itself, and -- the reason that matters most --
// they never have to fetch anything, which is what a Content-Security-
// Policy of `default-src 'self'` with no runtime data source would make
// awkward.
//
// Two things the renderer does that a plain `<ReactMarkdown>` does not:
//
//   - Heading ids. `docs-check` validates every `#anchor` link in the
//     tree against the slugs `internal/docsmeta` computes, so the ids
//     here have to be the same slugs or a link that passes the check
//     still lands nowhere. See lib/docs.slugify.
//
//   - Link rewriting. A link in the tree is a file path --
//     `../operator/config.md` -- because that is what resolves on disk
//     and on GitHub. In the browser it has to be the URL the manifest
//     gives that page. rewriteHref throws on one that resolves to a file
//     the manifest does not list, which fails the build rather than
//     shipping a link to the dashboard.

/** A hast node, as react-markdown hands one to a component. */
interface HastNode {
  type?: string;
  value?: string;
  tagName?: string;
  children?: HastNode[];
}

/** The text content of a heading, for slugging. */
function textOf(node: HastNode | undefined): string {
  if (!node) return '';
  if (node.type === 'text') return node.value ?? '';
  // Inline code contributes its text: docsmeta strips the backticks and
  // keeps what is between them.
  return (node.children ?? []).map(textOf).join('');
}

export interface DocsMarkdownProps {
  /** The page's Markdown, front matter already removed. */
  body: string;
  /** The page's docs-root-relative path, which relative links resolve against. */
  from: string;
  /** Path-to-URL for every page in the tree. */
  urls: Map<string, string>;
}

export function DocsMarkdown({ body, from, urls }: DocsMarkdownProps) {
  // One counter per rendered page, so a repeated heading gets the -1,
  // -2 suffix docsmeta gives it.
  const ids = new HeadingIds();

  const heading = (Tag: 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6') => {
    const Heading = ({ node, children }: { node?: HastNode; children?: React.ReactNode }) => {
      const id = ids.next(textOf(node));
      return (
        <Tag id={id || undefined}>
          {/* The permalink's "#" is drawn by CSS rather than written
              here, so it stays out of the heading's text: a heading
              whose textContent began with "#" would read that way to
              anything that quotes it -- a test, a table of contents, a
              search index. */}
          {id ? (
            <a href={`#${id}`} className="docs-anchor" aria-label="Link to this section" />
          ) : null}
          {children}
        </Tag>
      );
    };
    Heading.displayName = `Docs${Tag.toUpperCase()}`;
    return Heading;
  };

  const components: Components = {
    h1: heading('h1'),
    h2: heading('h2'),
    h3: heading('h3'),
    h4: heading('h4'),
    h5: heading('h5'),
    h6: heading('h6'),
    a: ({ href, children }) => {
      const to = rewriteHref(href ?? '', from, urls);
      const offSite = /^https?:/i.test(to);
      return (
        <a href={to} {...(offSite ? { target: '_blank', rel: 'noreferrer' } : {})}>
          {children}
        </a>
      );
    },
  };

  return (
    <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
      {body}
    </ReactMarkdown>
  );
}
