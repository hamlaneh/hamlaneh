import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

import App from "./App";
import { forgetSsoLanding, OIDC_START_PATH, providerRedirect } from "./auth/sso";
import { isolateAuto } from "./i18n/bidi";
import i18n from "./i18n";
import en from "./locales/en/common.json";
import fa from "./locales/fa/common.json";
import {
  clearMockPassword,
  FIXTURE_CREDENTIALS,
  FIXTURE_OIDC_REDIRECT_URL,
  FIXTURE_SSO_PROVIDER_NAME,
  FIXTURE_TOTP_CODE,
  linkMockSso,
  resetMockAuth,
  setMockInstance,
  startMockSsoChallenge,
} from "./mocks/handlers";
import { server } from "./mocks/node";

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  // The landing is read once per page load; each case is a fresh load.
  forgetSsoLanding();
});

afterEach(async () => {
  server.resetHandlers();
  resetMockAuth();
  window.history.replaceState({}, "", "/");
  await i18n.changeLanguage("en");
});

afterAll(() => {
  server.close();
});

/** The provider's name as it is rendered — isolated, so it keeps its own direction. */
const PROVIDER = isolateAuto(FIXTURE_SSO_PROVIDER_NAME);

/**
 * Lands on the application root the way the OIDC callback does — every outcome
 * goes to the same place and differs only by this query parameter.
 */
function arriveFromCallback(query: string) {
  window.history.replaceState({}, "", `/?${query}`);
  forgetSsoLanding();
}

async function showSignIn(locale: typeof en | typeof fa = en) {
  render(<App />);
  await screen.findByRole("heading", { name: locale.login.title });
}

/** Signs in and opens Settings, which starts on Security. */
async function openSecurity(
  user: UserEvent,
  credentials: { identifier: string; password: string } = FIXTURE_CREDENTIALS,
) {
  render(<App />);
  await screen.findByRole("heading", { name: en.login.title });
  await user.type(screen.getByLabelText(en.login.identifierLabel), credentials.identifier);
  await user.type(screen.getByLabelText(en.login.passwordLabel), credentials.password);
  await user.click(screen.getByRole("button", { name: en.login.submit }));
  await screen.findByRole("navigation", { name: en.chat.sidebar.label });
  await user.click(screen.getByRole("button", { name: en.chat.footer.account }));
  await screen.findByRole("dialog", { name: en.settings.title });
}

describe("the sign-in screen's single sign-on button", () => {
  it("is absent on an instance with no provider configured", async () => {
    setMockInstance({ sso: { enabled: false } });
    await showSignIn();

    // The instance document has to have arrived before absence means anything:
    // the reset link is the other thing it decides, so waiting for that proves
    // the answer was read rather than merely not-yet-fetched.
    await screen.findByRole("button", { name: en.login.forgotPassword });
    // By href, not by label: a button that renders with the generic fallback
    // name is still a door to a provider that does not exist, and asserting on
    // the provider's name would let that through.
    expect(screen.queryAllByRole("link").map((link) => link.getAttribute("href"))).not.toContain(
      OIDC_START_PATH,
    );
  });

  it("is a link to the start endpoint, named after the provider", async () => {
    await showSignIn();

    const link = await screen.findByRole("link", {
      name: en.login.sso.continueWith.replace("{{provider}}", PROVIDER),
    });
    // A link, not a button: the whole flow depends on the browser following
    // the server's 302 to the provider and carrying the transaction cookie
    // back with the callback. A fetch would swallow both.
    expect(link).toHaveAttribute("href", OIDC_START_PATH);
  });

  it("names a Latin provider inside the Persian sentence without flipping it", async () => {
    await i18n.changeLanguage("fa");
    await showSignIn(fa);

    const link = await screen.findByRole("link", {
      name: fa.login.sso.continueWith.replace("{{provider}}", PROVIDER),
    });
    // First-strong isolate around the name, so "Fixture Identity" reads
    // left-to-right inside a right-to-left sentence.
    expect(link.textContent).toContain(PROVIDER);
  });
});

describe("a single sign-on attempt that came back refused", () => {
  it.for([
    ["sso_account_exists", en.login.sso.error.sso_account_exists],
    ["sso_account_unknown", en.login.sso.error.sso_account_unknown],
    ["sso_failed", en.login.sso.error.sso_failed],
  ] as const)("says what happened for %s, and only that", async ([code, message]) => {
    arriveFromCallback(`sso_error=${code}`);
    await showSignIn();

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
  });

  it("strips the code from the address bar once it has been read", async () => {
    arriveFromCallback("sso_error=sso_failed");
    await showSignIn();

    await screen.findByRole("alert");
    // A refresh must not re-show a failure that already happened, and a copied
    // link must not carry it to whoever it is pasted to.
    expect(window.location.search).toBe("");
    expect(window.location.href).not.toContain("sso_error");
  });

  it("keeps the rest of the query and reports an unrecognised code as a plain failure", async () => {
    arriveFromCallback("keep=me&sso_error=something-the-provider-said");
    await showSignIn();

    // Never echo a code this client does not know: the contract keeps
    // provider-supplied text out of the URL and the page entirely.
    expect(await screen.findByRole("alert")).toHaveTextContent(en.login.sso.error.sso_failed);
    expect(screen.queryByText(/something-the-provider-said/u)).toBeNull();
    expect(window.location.search).toBe("?keep=me");
  });
});

describe("a single sign-on that came back owing a second factor", () => {
  it("lands on the challenge screen rather than the password form", async () => {
    startMockSsoChallenge();
    arriveFromCallback("sso=totp");
    render(<App />);

    // The challenge, not the form: the password step already happened at the
    // provider, and offering it again would ask for a credential this account
    // may not even have.
    await screen.findByRole("heading", { name: en.totp.title });
    expect(screen.queryByLabelText(en.login.passwordLabel)).toBeNull();
    // Nothing is passed to the screen and nothing needs to be: the challenge
    // travels in the cookie the callback set.
    expect(window.location.search).toBe("");
  });

  it("signs in when the code is accepted", async () => {
    const user = userEvent.setup({ delay: null });
    startMockSsoChallenge();
    arriveFromCallback("sso=totp");
    render(<App />);

    const cells = within(
      await screen.findByRole("group", { name: en.totp.codeLabel }),
    ).getAllByRole("textbox");
    for (const [index, digit] of Array.from(FIXTURE_TOTP_CODE).entries()) {
      const cell = cells[index];
      if (cell === undefined) {
        throw new Error(`the code input has no cell ${String(index)}`);
      }
      await user.type(cell, digit);
    }
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    // The session the callback did not mint is minted here, against the
    // challenge cookie alone — the screen never learned how it was reached.
    await screen.findByRole("navigation", { name: en.chat.sidebar.label });
  });

  it("shows the challenge for a method it does not recognise, never the value", async () => {
    startMockSsoChallenge();
    arriveFromCallback("sso=webauthn");
    render(<App />);

    // A second factor really is owed — the cookie is set — so falling back
    // inside the closed set beats dropping the reader on the password form.
    await screen.findByRole("heading", { name: en.totp.title });
    expect(screen.queryByText(/webauthn/iu)).toBeNull();
    expect(window.location.search).toBe("");
  });
});

describe("Settings → Security, single sign-on", () => {
  it("is absent on an instance with no provider and nothing linked", async () => {
    setMockInstance({ sso: { enabled: false } });
    const user = userEvent.setup({ delay: null });
    await openSecurity(user);

    await screen.findByRole("heading", { name: en.settings.totp.cardTitle });
    expect(screen.queryByRole("heading", { name: en.settings.sso.cardTitle })).toBeNull();
  });

  it("connects by asking for the redirect and then leaving for the provider", async () => {
    const follow = vi
      .spyOn(providerRedirect, "follow")
      .mockImplementation(() => undefined);
    const user = userEvent.setup({ delay: null });
    await openSecurity(user);

    await user.click(
      await screen.findByRole("button", {
        name: en.settings.sso.connect.replace("{{provider}}", PROVIDER),
      }),
    );

    await waitFor(() => {
      expect(follow).toHaveBeenCalledWith(FIXTURE_OIDC_REDIRECT_URL);
    });
    follow.mockRestore();
  });

  it("disconnects a linked account", async () => {
    const user = userEvent.setup({ delay: null });
    await openSecurity(user);
    linkMockSso();
    // The card reads the flag when Security mounts, so leave and come back.
    await user.click(screen.getByRole("button", { name: en.settings.close }));
    await user.click(screen.getByRole("button", { name: en.chat.footer.account }));

    await user.click(
      await screen.findByRole("button", {
        name: en.settings.sso.disconnect.replace("{{provider}}", PROVIDER),
      }),
    );

    expect(
      await screen.findByRole("button", {
        name: en.settings.sso.connect.replace("{{provider}}", PROVIDER),
      }),
    ).toBeInTheDocument();
  });

  it("explains the refusal when the account has no password to fall back on", async () => {
    const user = userEvent.setup({ delay: null });
    await openSecurity(user);
    linkMockSso();
    clearMockPassword();
    await user.click(screen.getByRole("button", { name: en.settings.close }));
    await user.click(screen.getByRole("button", { name: en.chat.footer.account }));

    await user.click(
      await screen.findByRole("button", {
        name: en.settings.sso.disconnect.replace("{{provider}}", PROVIDER),
      }),
    );

    // Not "something went wrong": the account has no password, so this link is
    // its only way in and only an administrator can change that.
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.settings.sso.error.noPassword,
    );
    // And the link is still there — the refusal changed nothing.
    expect(
      screen.getByRole("button", {
        name: en.settings.sso.disconnect.replace("{{provider}}", PROVIDER),
      }),
    ).toBeInTheDocument();
  });
});
