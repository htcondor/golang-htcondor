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
  await page.goto("/info");

  // The panel exists at all, which is the half the stubbed test could
  // not reach: with no gateway /api/v1/ssh/ca answers 503 and the panel
  // correctly hides itself, so this appearing proves a CA was minted.
  await expect(page.getByText("SSH access")).toBeVisible();

  // What a user is given to trust, from the real CA.
  const body = page.locator("body");
  await expect(body).toContainText("@cert-authority");
  await expect(body).toContainText("SHA256:");

  await page.locator("textarea").fill(pub);
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
});
