import { describe, expect, it } from 'vitest';

import {
  HeadingIds,
  REPO_BLOB,
  REPO_TREE,
  resolveTarget,
  rewriteHref,
  slugForUrl,
  slugify,
  urlByPath,
  type DocNav,
} from './docs';

const nav: DocNav = {
  guides: [
    {
      audience: 'user',
      title: 'User guide',
      pages: [
        { path: 'user/index.md', url: '/docs/user', title: 'User guide', audience: 'user', order: 10 },
        { path: 'user/jobs.md', url: '/docs/user/jobs', title: 'Jobs', audience: 'user', order: 12 },
      ],
    },
    {
      audience: 'operator',
      title: 'Operator guide',
      pages: [
        {
          path: 'operator/config.md',
          url: '/docs/operator/config',
          title: 'Configuration',
          audience: 'operator',
          order: 10,
        },
      ],
    },
  ],
};
const urls = urlByPath(nav);

// The slugs have to be the ones internal/docsmeta computes, because
// those are what `make docs-check` validates every anchor link against.
// The cases below are the ones where a plausible alternative slugger
// disagrees.
describe('slugify', () => {
  it('lowercases and hyphenates', () => {
    expect(slugify('Two hubs')).toBe('two-hubs');
  });
  it('strips inline code backticks and keeps the text', () => {
    expect(slugify('`HTTP_API_LISTEN_ADDR`')).toBe('http_api_listen_addr');
  });
  it('reduces a link to its text', () => {
    expect(slugify('See [the guide](../user/index.md)')).toBe('see-the-guide');
  });
  it('drops punctuation rather than hyphenating it', () => {
    expect(slugify("What's served, and when?")).toBe('whats-served-and-when');
  });
  it('drops emphasis markers', () => {
    expect(slugify('*Not* every setting')).toBe('not-every-setting');
  });
  it('drops non-ASCII letters', () => {
    expect(slugify('Café au lait')).toBe('caf-au-lait');
  });
  it('keeps hyphens and underscores', () => {
    expect(slugify('multi-ap_mode')).toBe('multi-ap_mode');
  });
});

describe('HeadingIds', () => {
  it('suffixes a repeated heading the way docsmeta does', () => {
    const ids = new HeadingIds();
    expect(ids.next('Configuration')).toBe('configuration');
    expect(ids.next('Configuration')).toBe('configuration-1');
    expect(ids.next('Configuration')).toBe('configuration-2');
  });
  it('gives a heading with no sluggable characters no id', () => {
    expect(new HeadingIds().next('!!!')).toBe('');
  });
});

describe('resolveTarget', () => {
  it('resolves a sibling page', () => {
    expect(resolveTarget('operator/auth.md', 'config.md')).toEqual({
      kind: 'page',
      path: 'operator/config.md',
    });
  });
  it('resolves across guides', () => {
    expect(resolveTarget('user/jobs.md', '../operator/config.md')).toEqual({
      kind: 'page',
      path: 'operator/config.md',
    });
  });
  it('calls a link out of the docs tree a repository link', () => {
    expect(resolveTarget('developer/api.md', '../../design_notes/RATE_LIMITING.md')).toEqual({
      kind: 'repo',
      path: 'design_notes/RATE_LIMITING.md',
    });
  });
  it('keeps a trailing slash, which distinguishes a directory', () => {
    expect(resolveTarget('developer/api.md', '../../examples/')).toEqual({
      kind: 'repo',
      path: 'examples/',
    });
  });
  it('refuses to climb above the repository', () => {
    expect(() => resolveTarget('user/jobs.md', '../../../etc/passwd')).toThrow(/above the repository/);
  });
});

describe('rewriteHref', () => {
  it('leaves an absolute URL alone', () => {
    expect(rewriteHref('https://example.edu/x', 'user/jobs.md', urls)).toBe('https://example.edu/x');
  });
  it('leaves a site-absolute path alone', () => {
    expect(rewriteHref('/jobs', 'user/jobs.md', urls)).toBe('/jobs');
  });
  it('leaves a same-page anchor alone', () => {
    expect(rewriteHref('#the-database', 'user/jobs.md', urls)).toBe('#the-database');
  });
  it('turns a relative page link into the manifest URL', () => {
    expect(rewriteHref('../operator/config.md', 'user/jobs.md', urls)).toBe('/docs/operator/config');
  });
  it('keeps the fragment on a cross-page link', () => {
    expect(rewriteHref('../operator/config.md#settings', 'user/jobs.md', urls)).toBe(
      '/docs/operator/config#settings',
    );
  });
  it('resolves an index page to the URL with no /index', () => {
    expect(rewriteHref('index.md', 'user/jobs.md', urls)).toBe('/docs/user');
  });
  it('sends a link out of the tree to the repository', () => {
    expect(rewriteHref('../../design_notes/RATE_LIMITING.md', 'user/jobs.md', urls)).toBe(
      `${REPO_BLOB}/design_notes/RATE_LIMITING.md`,
    );
  });
  it('sends a directory out of the tree to the repository tree view', () => {
    expect(rewriteHref('../../examples/', 'user/jobs.md', urls)).toBe(`${REPO_TREE}/examples`);
  });
  it('keeps a fragment on a repository link', () => {
    expect(rewriteHref('../../webapi/httpserver/README.md#configuration', 'user/jobs.md', urls)).toBe(
      `${REPO_BLOB}/webapi/httpserver/README.md#configuration`,
    );
  });
  // The failure this whole mapping exists to catch: the file is on disk,
  // so `make docs-check` is happy, but no page serves it and the reader
  // would get the dashboard.
  it('throws on a docs file the manifest does not list as a page', () => {
    expect(() => rewriteHref('../README.md', 'user/jobs.md', urls)).toThrow(
      /docs\/nav\.json does not list as a page/,
    );
  });
});

describe('slugForUrl', () => {
  it('splits a page URL into route parameters', () => {
    expect(slugForUrl('/docs/operator/config')).toEqual(['operator', 'config']);
    expect(slugForUrl('/docs/user')).toEqual(['user']);
  });
  it('refuses a URL outside the documentation prefix', () => {
    expect(() => slugForUrl('/jobs')).toThrow(/not under \/docs/);
  });
});
