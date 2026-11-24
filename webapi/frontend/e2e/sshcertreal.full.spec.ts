import { readFileSync, writeFileSync } from "node:fs";

import { expect, test } from "@playwright/test";

import { adminPassword, loginAsAdmin } from "./fixtures/login";
import { serverLog } from "./fixtures/serverlog";

// The SSH certificate panel driven against a REAL certificate authority.
//
// sshcert.full.spec.ts stubbed /api/v1/ssh/ca and /api/v1/ssh/certificate
// because demo mode had no gateway and therefore no CA to mint with. With
// the gateway turned on (HTTP_API_SSH_GATEWAY_ADDRESS plus a KEK), the
// panel's real branch runs: a real CA is published, a real certificate is
// signed, and the certificate this writes out is one the gateway will
// actually accept -- which is what the sftp(1) leg of this run then uses.
//
// Two optional env knobs, for pairing this with a shell step outside
// Playwright that wants a usable certificate:
//   E2E_PUBKEY_FILE  a public key to have signed, instead of a throwaway
//   E2E_CERT_OUT     where to write the certificate it comes back with
//
// Credentials come from the harness's server log, like every other full
// spec, and NOT from demo mode's credentials file: in CI the server runs
// inside a container, so that file is not on the host Playwright runs
// on, and reading it would fail there while passing locally.

const pubKeyPath = process.env.E2E_PUBKEY_FILE;
const certOut = process.env.E2E_CERT_OUT;

// A syntactically valid key to sign when no file was named. The private
// half does not exist anywhere, which is the point: this spec tests that
// a key presented to the panel comes back signed, not that anybody can
// use the result.
const EPHEMERAL_PUBLIC_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEraR6NBbL5MeTWlBQ5wSEtSTfmRNHPTCCi3dEOJSjZE spec@playwright";

const password = adminPassword(serverLog());

test("a real certificate authority signs a real key", async ({ page, request }) => {
  // Skipped, not failed, where this deployment has no gateway: with
  // HTTP_API_SSH_GATEWAY_ADDRESS unset there is no CA to sign with, the
  // panel correctly hides itself, and there is nothing here to test.
  // sshcert.full.spec.ts covers that branch against a stub.
  const ca = await request.get("/api/v1/ssh/ca");
  test.skip(
    ca.status() === 503,
    "this server has no SSH certificate authority; set HTTP_API_SSH_GATEWAY_ADDRESS and HTTP_API_KEK_FILE",
  );

  const pub = (
    pubKeyPath ? readFileSync(pubKeyPath, "utf8") : EPHEMERAL_PUBLIC_KEY
  ).trim();

  // Captured from the wire, not read off the page: the panel offers the
  // certificate as a download, and what the gateway has to accept is the
  // exact bytes the endpoint signed.
  const issued: { certificate?: string; principal?: string } = {};
  page.on("response", async (resp) => {
    if (resp.url().includes("/api/v1/ssh/certificate") && resp.status() === 200) {
      Object.assign(issued, await resp.json());
    }
  });

  await loginAsAdmin(page, password);
  await page.goto("/connectors");

  // The panel exists at all, which is the half the stubbed test could
  // not reach: with no gateway /api/v1/ssh/ca answers 503 and the panel
  // correctly hides itself, so this appearing proves a CA was minted.
  await expect(page.getByText("Connect over SSH")).toBeVisible();

  // What a user is given to trust, from the real CA.
  const body = page.locator("body");
  await expect(body).toContainText("@cert-authority");
  await expect(body).toContainText("SHA256:");

  await page.getByTestId("ssh-public-key").fill(pub);
  await page.getByRole("button", { name: /issue certificate/i }).click();

  await expect(body).toContainText("id_ed25519-cert.pub");
  const download = page.getByRole("link", { name: /download/i });
  await expect(download).toBeVisible();

  await expect
    .poll(() => Boolean(issued.certificate), { timeout: 10_000 })
    .toBe(true);
  expect(issued.certificate).toContain("ssh-ed25519-cert-v01@openssh.com");
  // The principal is who the gateway will authorise, so a certificate
  // naming nobody is worse than none. Not compared against a literal:
  // who is logged in depends on the deployment, and demo mode's "admin"
  // is not a name a real access point has.
  expect(issued.principal, "the certificate names no principal").toBeTruthy();

  // Written out only when a caller asked for it, so this spec stands
  // alone in CI and still hands a usable certificate to a shell step
  // that wants to drive sftp(1) with it.
  if (certOut) {
    writeFileSync(certOut, `${issued.certificate}\n`, { mode: 0o600 });
  }

  // The certificate is RECORDED, which is what makes a long-lived one
  // safe to hand out. A certificate that worked but appeared in no list
  // would be one the Revoke button cannot reach, with nothing telling
  // its holder that theirs is the one which cannot be withdrawn.
  // Located by the comment off the key that was actually signed, which
  // is what the table labels the row with. Derived rather than
  // hardcoded, because E2E_PUBKEY_FILE may have supplied a key whose
  // comment is somebody else's -- or absent, which the page renders as
  // "No comment".
  const comment = pub.split(/\s+/)[2] ?? "No comment";
  const row = page.locator("tr", { hasText: comment }).first();
  await expect(row).toBeVisible({ timeout: 10_000 });
  await expect(row).toContainText("Active");
});

// Withdrawing one, against a real CA and a real record.
//
// The whole reason certificates are recorded: before this, expiry was
// the only way one ever stopped working, which is why the cap was a day
// and why nothing unattended could use the feature at all.
test("an issued certificate can be withdrawn", async ({ page, request }) => {
  const ca = await request.get("/api/v1/ssh/ca");
  test.skip(
    ca.status() === 503,
    "this server has no SSH certificate authority; set HTTP_API_SSH_GATEWAY_ADDRESS and HTTP_API_KEK_FILE",
  );

  await loginAsAdmin(page, password);
  await page.goto("/connectors");
  await expect(page.getByText("Connect over SSH")).toBeVisible();

  // A key of its own, so this does not race the spec above over which
  // row is whose.
  const pub =
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEraR6NBbL5MeTWlBQ5wSEtSTfmRNHPTCCi3dEOJSjZE revokeme@playwright";
  await page.getByTestId("ssh-public-key").fill(pub);
  await page.getByRole("button", { name: /issue certificate/i }).click();

  const row = page.locator("tr", { hasText: "revokeme@playwright" }).first();
  await expect(row).toBeVisible({ timeout: 10_000 });
  await expect(row).toContainText("Active");

  // By ROLE, not by text: getByText matches the button and every
  // wrapper whose text content is also "Revoke", which is a strict-mode
  // violation rather than a click. The accessible name is
  // ConfirmButton's title.
  const revoke = row.getByRole("button", {
    name: "Refuse this certificate from now on",
  });

  // ConfirmButton asks first; the first click is the ask.
  await revoke.click();
  await row.getByRole("button", { name: "Confirm" }).click();

  // Revoked, and the button is gone with it -- there is nothing left to
  // withdraw, and offering one would invite a second press that does
  // nothing.
  await expect(row).toContainText("Revoked", { timeout: 10_000 });
  await expect(revoke).toHaveCount(0);
});
