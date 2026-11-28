import { expect, test } from '@playwright/test';

import { installApiFixtures } from './fixtures/api';

// The documentation route, in a browser, against the built static
// export -- which is the only place two of these can fail.
//
// A link between two documentation pages is a file path on disk
// (`../operator/config.md`). `make docs-check` validates it as a path
// and the unit tests validate the rewrite, but whether the rewritten
// URL is a page the export actually wrote is a question only the export
// answers, and the way it fails is a 404 or, on the Go server, the
// dashboard.
//
// The other is the shell. Documentation is public; a reader without a
// session has to get the page rather than a redirect to the identity
// provider.

test('a documentation page renders, with the guide navigation', async ({ page }) => {
  await installApiFixtures(page);
  await page.goto('/docs/user');

  const article = page.getByTestId('docs-article');
  await expect(article.getByRole('heading', { level: 1 })).toHaveText('User guide');
  // The navigation is docs/nav.json: all three guides, by their titles.
  const nav = page.getByTestId('docs-nav');
  await expect(nav.getByRole('heading', { name: 'User guide' })).toBeVisible();
  await expect(nav.getByRole('heading', { name: 'Operator guide' })).toBeVisible();
  await expect(nav.getByRole('heading', { name: 'Developer and API reference' })).toBeVisible();

  await nav.getByRole('link', { name: 'Submitting and watching a job' }).click();
  await expect(page).toHaveURL(/\/docs\/user\/jobs$/);
  await expect(article.getByRole('heading', { level: 1 })).toHaveText(
    'Submitting and watching a job',
  );
});

test('a link written as a file path reaches the page it names', async ({ page }) => {
  await installApiFixtures(page);
  await page.goto('/docs/user/files');

  // Written in the Markdown as ../operator/config.md. If the export did
  // not write that page, this is where it shows.
  const link = page.getByTestId('docs-article').getByRole('link', { name: 'htcondordb mirror' });
  await expect(link).toHaveAttribute('href', '/docs/operator/config');
  await link.click();
  await expect(page).toHaveURL(/\/docs\/operator\/config$/);
  await expect(page.getByTestId('docs-article').getByRole('heading', { level: 1 })).toBeVisible();
});

test('a reader with no session gets the documentation, not the login page', async ({ page }) => {
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({ json: { authenticated: false } }),
  );
  await page.route('**/api/v1/webui/branding', (route) => route.fulfill({ json: {} }));
  await page.route('**/login**', (route) => route.fulfill({ status: 404, body: 'no login here' }));

  await page.goto('/docs/operator');
  await expect(page.getByTestId('docs-article').getByRole('heading', { level: 1 })).toHaveText(
    'HTTP API + MCP server',
  );
  expect(page.url()).toContain('/docs/operator');
});
