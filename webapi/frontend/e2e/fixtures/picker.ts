import { readFileSync } from 'node:fs';
import path from 'node:path';
import type { Page } from '@playwright/test';

// A file chooser, with Google's side of it stood in for.
//
// Two things are substituted and one deliberately is not:
//
//   - /api/v1/pickers and the connector token come from the fixture,
//     because the static export has no server.
//   - https://apis.google.com/js/api.js is answered with a stand-in
//     gapi implementing the documented Picker surface. The real one
//     cannot run in a hermetic test: it needs Google, a real developer
//     key, and a real signed-in account.
//   - The ADAPTER is the real file, read off disk from the Go server's
//     embedded copy. It is the piece most likely to be wrong -- it
//     speaks a third-party API -- so a test that substituted it would
//     be testing a second adapter that only exists in tests.
//
// What the stand-in gapi answers is drawn from Google's Picker
// documentation: gapi.load('picker'), PickerBuilder's chaining setters,
// and a callback whose data is keyed by google.picker.Response.* with
// an action from google.picker.Action.*.

const ADAPTER = path.join(
  __dirname,
  '..',
  '..',
  '..',
  'httpserver',
  'pickerjs',
  'gdrive.js',
);

/** What the chooser should do when the page opens it. */
export type PickerPlan =
  | { action: 'picked'; files: { id: string; name: string }[] }
  | { action: 'cancel' }
  | { action: 'error'; message: string };

export interface PickerFixtureOptions {
  /** What happens when the chooser opens. Defaults to picking nothing. */
  plan?: PickerPlan;
  /**
   * Answer the token endpoint with 409 instead of a token, as the
   * server does for somebody who has not authorized yet.
   */
  notConnected?: boolean;
  /** Tell 409 apart: a grant that existed and has since died. */
  needsConsent?: boolean;
  /** Omit the developer key, as a misconfigured chooser would. */
  withoutAPIKey?: boolean;
}

export const PICKER_SERVICE = 'webapi-builtin-gdrive';

/**
 * Install one configured Google Drive chooser.
 *
 * Call after installApiFixtures: the broad /api/v1/** route it adds
 * answers 501 for /api/v1/pickers, and Playwright runs the most
 * recently added matching route first.
 */
export async function installPickerFixtures(
  page: Page,
  opts: PickerFixtureOptions = {},
) {
  const plan: PickerPlan = opts.plan ?? { action: 'picked', files: [] };

  // The plan is read by the stand-in gapi at open time.
  await page.addInitScript((p) => {
    (window as unknown as { __pickerPlan: PickerPlan }).__pickerPlan =
      p as PickerPlan;
  }, plan);

  await page.route('**/api/v1/pickers', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([
        {
          name: 'drive',
          provider: 'gdrive',
          connector: 'builtin:gdrive',
          service: PICKER_SERVICE,
          method: 'gdrive2',
          script: '/picker/gdrive.js',
          ...(opts.withoutAPIKey ? {} : { api_key: 'AIzaSyFIXTURE' }),
          app_id: '1234567890',
        },
      ]),
    }),
  );

  await page.route('**/api/v1/connect/*/token', (route) => {
    if (opts.notConnected) {
      return route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({
          connector: 'builtin:gdrive',
          connected: false,
          needs_consent: opts.needsConsent === true,
          connect_url: '/api/v1/connect/builtin:gdrive',
        }),
      });
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        access_token: 'ya29.fixture-access-token',
        expires_in: 3599,
        connector: 'builtin:gdrive',
      }),
    });
  });

  // The shipped adapter, not a copy of it.
  await page.route('**/picker/gdrive.js', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/javascript; charset=utf-8',
      body: readFileSync(ADAPTER, 'utf8'),
    }),
  );

  await page.route('https://apis.google.com/js/api.js', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/javascript; charset=utf-8',
      body: standInGapi,
    }),
  );
}

/**
 * A stand-in for Google's api.js, implementing the Picker surface the
 * adapter uses.
 *
 * It records what the adapter asked for on window.__pickerCalls so a
 * test can assert that the developer key and the token reached it --
 * the two settings whose absence makes a real chooser render and then
 * list nothing, which is indistinguishable from it working until
 * somebody tries.
 */
const standInGapi = `
(function () {
  "use strict";
  var calls = { built: [] };
  window.__pickerCalls = calls;

  function Builder() {
    this.opts = { views: [], features: [] };
  }
  Builder.prototype.setOAuthToken = function (t) { this.opts.token = t; return this; };
  Builder.prototype.setDeveloperKey = function (k) { this.opts.developerKey = k; return this; };
  Builder.prototype.setAppId = function (a) { this.opts.appId = a; return this; };
  Builder.prototype.addView = function (v) { this.opts.views.push(v); return this; };
  Builder.prototype.enableFeature = function (f) { this.opts.features.push(f); return this; };
  Builder.prototype.setCallback = function (cb) { this.opts.callback = cb; return this; };
  Builder.prototype.build = function () {
    var opts = this.opts;
    calls.built.push({
      token: opts.token,
      developerKey: opts.developerKey,
      appId: opts.appId,
      views: opts.views.length,
      features: opts.features.slice(),
    });
    return {
      setVisible: function (visible) {
        if (!visible) return;
        var plan = window.__pickerPlan || { action: "picked", files: [] };
        // Asynchronously, as a real chooser answers a person.
        setTimeout(function () {
          if (plan.action === "cancel") {
            opts.callback({ action: "cancel" });
            return;
          }
          if (plan.action === "error") {
            // A real chooser has no error action; it fails earlier, in
            // the module load. See the loadFails test, which breaks
            // that instead.
            throw new Error(plan.message);
          }
          opts.callback({
            action: "picked",
            docs: (plan.files || []).map(function (f) {
              return { id: f.id, name: f.name, mimeType: "text/csv" };
            }),
          });
        }, 0);
      },
    };
  };

  function DocsView() {}
  DocsView.prototype.setIncludeFolders = function () { return this; };
  DocsView.prototype.setSelectFolderEnabled = function () { return this; };

  window.google = window.google || {};
  window.google.picker = {
    PickerBuilder: Builder,
    DocsView: DocsView,
    ViewId: { DOCS: "docs" },
    Feature: { MULTISELECT_ENABLED: "multiselect" },
    // The adapter reads the callback's data through these keys, so the
    // keys the stand-in answers to have to be the documented ones.
    Response: { ACTION: "action", DOCUMENTS: "docs" },
    Action: { PICKED: "picked", CANCEL: "cancel" },
  };

  window.gapi = {
    load: function (name, cfg) {
      calls.loaded = name;
      if (name !== "picker") {
        cfg.onerror && cfg.onerror();
        return;
      }
      setTimeout(function () { cfg.callback(); }, 0);
    },
  };
})();
`;

/** The reference the page should produce for a chosen file. */
export function expectedReference(id: string, name: string): string {
  return `${PICKER_SERVICE}+gdrive2://${id}/${name}`;
}
