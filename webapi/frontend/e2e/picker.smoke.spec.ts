import { expect, test } from '@playwright/test';

import { installApiFixtures } from './fixtures/api';
import { expectedReference, installPickerFixtures } from './fixtures/picker';

// Picking a job's input out of a Google Drive account.
//
// The chooser runs for real here: the page loads the adapter the Go
// server embeds, and the adapter drives a stand-in gapi that answers
// the documented Picker surface. So these tests cover the page, the
// adapter and the contract between them -- the parts that a unit test
// of either side alone cannot reach.

/**
 * Publish one template with no columns, so the page is submittable.
 *
 * The shared fixture publishes none, which leaves the submit-file body
 * empty and the Submit button correctly disabled.
 */
async function installTemplate(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/templates', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        templates: [
          {
            id: 'sleep',
            name: 'Sleep',
            columns: [],
            contents:
              'executable = /bin/sleep\ntransfer_executable = false\narguments = 5\n',
            source: 'builtin',
          },
        ],
      }),
    }),
  );
}

async function openSubmit(page: import('@playwright/test').Page) {
  await page.goto('/submit');
  // The chooser lives in Common Inputs, which is on the page from the
  // start; no template is needed to reach it.
  await expect(page.getByTestId('picker-open-drive')).toBeVisible();
}

test.describe('with one chooser configured', () => {
  test.beforeEach(async ({ page }) => {
    await installApiFixtures(page);
  });

  test('a chosen file becomes a submit-file reference', async ({ page }) => {
    await installPickerFixtures(page, {
      plan: { action: 'picked', files: [{ id: 'FILEID1', name: 'data.csv' }] },
    });
    await openSubmit(page);

    await page.getByTestId('picker-open-drive').click();

    const chosen = page.getByTestId('picker-chosen');
    await expect(chosen).toContainText('data.csv');
    // The reference is what ends up in transfer_input_files, so it is
    // shown rather than a size: the file id is also the only way to
    // tell two files of the same name apart.
    await expect(chosen).toContainText(expectedReference('FILEID1', 'data.csv'));
  });

  // The two settings whose absence makes a real chooser render and then
  // list nothing, which looks exactly like it working until somebody
  // tries to pick something.
  test('the token and the developer key reach the chooser', async ({ page }) => {
    await installPickerFixtures(page, {
      plan: { action: 'picked', files: [{ id: 'F', name: 'a.csv' }] },
    });
    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();
    await expect(page.getByTestId('picker-chosen')).toContainText('a.csv');

    const built = await page.evaluate(
      () =>
        (
          window as unknown as {
            __pickerCalls: { built: { token?: string; developerKey?: string; appId?: string }[] };
          }
        ).__pickerCalls.built,
    );
    expect(built).toHaveLength(1);
    expect(built[0].token).toBe('ya29.fixture-access-token');
    expect(built[0].developerKey).toBe('AIzaSyFIXTURE');
    expect(built[0].appId).toBe('1234567890');
  });

  test('picking several files adds them all', async ({ page }) => {
    await installPickerFixtures(page, {
      plan: {
        action: 'picked',
        files: [
          { id: 'A', name: 'a.csv' },
          { id: 'B', name: 'b.csv' },
        ],
      },
    });
    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();

    const rows = page.getByTestId('picker-chosen').locator('li');
    await expect(rows).toHaveCount(2);
  });

  test('a chosen file can be removed again', async ({ page }) => {
    await installPickerFixtures(page, {
      plan: { action: 'picked', files: [{ id: 'F', name: 'data.csv' }] },
    });
    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();
    await expect(page.getByTestId('picker-chosen')).toBeVisible();

    await page.getByRole('button', { name: 'Remove data.csv' }).click();
    await expect(page.getByTestId('picker-chosen')).toHaveCount(0);
  });

  // Cancelling is not an error and must leave no trace: a person who
  // opens the chooser to look and closes it again has chosen nothing.
  test('cancelling leaves nothing behind', async ({ page }) => {
    await installPickerFixtures(page, { plan: { action: 'cancel' } });
    await openSubmit(page);

    await page.getByTestId('picker-open-drive').click();

    await expect(page.getByTestId('picker-chosen')).toHaveCount(0);
    await expect(page.getByTestId('picker-error')).toHaveCount(0);
    // The button is usable again rather than stuck on "Opening…".
    await expect(page.getByTestId('picker-open-drive')).toBeEnabled();
  });

  // The case the server cannot refuse, because the reference a space
  // produces reads as a shorter reference plus a second input file --
  // which is also what a submitter typing those tokens means. This is
  // the only place that has the real name.
  test('a name the submit file cannot carry is refused by name', async ({
    page,
  }) => {
    await installPickerFixtures(page, {
      plan: {
        action: 'picked',
        files: [
          { id: 'A', name: 'fine.csv' },
          { id: 'B', name: 'my data.csv' },
        ],
      },
    });
    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();

    const refused = page.getByTestId('picker-refused');
    await expect(refused).toContainText('my data.csv');
    await expect(refused).toContainText('space');
    // And the good one is still added: being refused all three files
    // over one bad name loses the choices that were fine.
    await expect(page.getByTestId('picker-chosen')).toContainText('fine.csv');
    await expect(page.getByTestId('picker-chosen').locator('li')).toHaveCount(1);
  });

  test('a macro in a name is refused too', async ({ page }) => {
    await installPickerFixtures(page, {
      plan: { action: 'picked', files: [{ id: 'A', name: '$(HOME).csv' }] },
    });
    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();

    await expect(page.getByTestId('picker-refused')).toContainText('dollar');
    await expect(page.getByTestId('picker-chosen')).toHaveCount(0);
  });

  // What the reference is FOR. Without this the rest only proves the
  // page can draw a list.
  test('the reference reaches the submitted body', async ({ page }) => {
    await installPickerFixtures(page, {
      plan: { action: 'picked', files: [{ id: 'FILEID1', name: 'data.csv' }] },
    });
    await installTemplate(page);

    let submitted = '';
    await page.route('**/api/v1/jobs', async (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      submitted = route.request().postData() ?? '';
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ job_ids: ['9001.0'], cluster: 9001 }),
      });
    });

    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();
    await expect(page.getByTestId('picker-chosen')).toContainText('data.csv');

    // "Run N copies" bypasses the table, which is otherwise one empty
    // row and correctly blocks the submission.
    await page.getByRole('button', { name: 'Run N copies' }).click();
    const submitBtn = page.getByRole('button', { name: /^Submit batch/ });
    await expect(submitBtn).toBeEnabled();
    await submitBtn.click();

    await expect.poll(() => submitted).toContain('transfer_input_files');
    expect(submitted).toContain(expectedReference('FILEID1', 'data.csv'));
  });
});

test.describe('without an authorization', () => {
  test.beforeEach(async ({ page }) => {
    await installApiFixtures(page);
  });

  // Nobody has consented the first time they use a chooser. Opening an
  // empty chooser would be the wrong answer: there is no token, so it
  // would render and list nothing.
  test('offers to authorize instead of opening an empty chooser', async ({
    page,
  }) => {
    await installPickerFixtures(page, { notConnected: true });
    await openSubmit(page);

    await page.getByTestId('picker-open-drive').click();

    const notice = page.getByTestId('picker-needs-connect');
    await expect(notice).toContainText('no Google Drive authorization');
    await expect(notice.getByRole('link', { name: 'Authorize it' })).toHaveAttribute(
      'href',
      '/api/v1/connect/builtin:gdrive',
    );
    // The chooser was never opened, so nothing was built.
    const calls = await page.evaluate(
      () => (window as unknown as { __pickerCalls?: unknown }).__pickerCalls,
    );
    expect(calls).toBeUndefined();
  });

  // A grant that existed and has since died reads differently, because
  // what the person should do about it is different.
  test('says so when an authorization has stopped working', async ({ page }) => {
    await installPickerFixtures(page, {
      notConnected: true,
      needsConsent: true,
    });
    await openSubmit(page);
    await page.getByTestId('picker-open-drive').click();

    await expect(page.getByTestId('picker-needs-connect')).toContainText(
      'no longer valid',
    );
  });
});

test.describe('when something is wrong', () => {
  test.beforeEach(async ({ page }) => {
    await installApiFixtures(page);
  });

  // A blocked script and a missing one look identical from the page, so
  // the message names the policy: it is the likely cause and the only
  // other evidence is a console entry nobody will think to look at.
  test('a chooser that cannot load says the policy may be why', async ({
    page,
  }) => {
    await installPickerFixtures(page);
    // Serve the adapter, but break the provider's script underneath it,
    // which is what a Content-Security-Policy missing apis.google.com
    // does.
    await page.route('https://apis.google.com/js/api.js', (route) =>
      route.abort('blockedbyclient'),
    );
    await openSubmit(page);

    await page.getByTestId('picker-open-drive').click();

    await expect(page.getByTestId('picker-error')).toContainText(
      'Content-Security-Policy',
    );
  });

  test('an adapter that does not load at all is reported', async ({ page }) => {
    await installPickerFixtures(page);
    await page.route('**/picker/gdrive.js', (route) => route.abort('failed'));
    await openSubmit(page);

    await page.getByTestId('picker-open-drive').click();

    await expect(page.getByTestId('picker-error')).toContainText(
      'could not load the file chooser',
    );
  });
});

// An access point with no chooser configured shows nothing about one.
// Somebody submitting a job should not read about a feature their
// access point does not offer.
test('no chooser configured means no chooser on the page', async ({ page }) => {
  await installApiFixtures(page);
  await page.route('**/api/v1/pickers', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '[]',
    }),
  );
  await page.goto('/submit');
  await expect(page.getByText('3. Common Inputs')).toBeVisible();
  await expect(page.getByTestId('picker-open-drive')).toHaveCount(0);
});
