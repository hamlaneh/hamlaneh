/**
 * Screen interactions the specs share.
 *
 * Everything is located by role and accessible name, and the names come from
 * the locale catalogue rather than from literals — a spec written this way
 * asserts what the user sees, in whichever language the project selected,
 * and it breaks when a control loses its accessible name (which is a real
 * regression) rather than when copy is edited (which is not).
 *
 * The app has almost no test ids, and that is fine: relying on role and name
 * means the suite also fails when a control stops being reachable to a
 * screen reader.
 */
import type { Locator, Page } from "@playwright/test";

import type { Translate } from "./i18n";

/**
 * The writing direction the browser actually resolved for an element.
 *
 * Asserting on a class name or on `dir` alone proves only that somebody
 * intended right-to-left; this is what the layout engine concluded.
 */
export function computedDirection(locator: Locator): Promise<string> {
  return locator.evaluate((element) => globalThis.getComputedStyle(element).direction);
}

export class App {
  constructor(
    readonly page: Page,
    private readonly t: Translate,
  ) {}

  /* ── sign-in ─────────────────────────────────────────────────────── */

  async gotoSignIn(): Promise<void> {
    await this.page.goto("/");
    await this.signInHeading.waitFor();
  }

  get signInHeading(): Locator {
    return this.page.getByRole("heading", { name: this.t("login.title") });
  }

  get identifierField(): Locator {
    return this.page.getByLabel(this.t("login.identifierLabel"));
  }

  get passwordField(): Locator {
    return this.page.getByLabel(this.t("login.passwordLabel"), { exact: true });
  }

  get signInButton(): Locator {
    return this.page.getByRole("button", { name: this.t("login.submit"), exact: true });
  }

  get forgotPasswordLink(): Locator {
    return this.page.getByRole("button", { name: this.t("login.forgotPassword") });
  }

  /** Fills both fields and submits. */
  async signIn(identifier: string, password: string): Promise<void> {
    await this.identifierField.fill(identifier);
    await this.passwordField.fill(password);
    await this.signInButton.click();
  }

  /** The form-level failure banner (NoticeBanner tone="danger"). */
  get errorBanner(): Locator {
    return this.page.getByRole("alert");
  }

  /** The form-level polite banner (warning / success / info tones). */
  get statusBanner(): Locator {
    return this.page.getByRole("status");
  }

  /* ── two-step verification ───────────────────────────────────────── */

  get twoStepHeading(): Locator {
    return this.page.getByRole("heading", { name: this.t("totp.title") });
  }

  get verifyButton(): Locator {
    return this.page.getByRole("button", { name: this.t("totp.submit") });
  }

  /**
   * Types a code into the six-cell input.
   *
   * The cells are one input each with maxLength=1 and focus advances on
   * every keystroke, so this types on the keyboard from the first cell
   * exactly as a person does — filling values directly would bypass the
   * component's own distribution logic and stop testing it.
   */
  async enterCode(code: string): Promise<void> {
    const first = this.page.getByLabel(this.t("password.otpCell", { index: 1, total: 6 }));
    await first.click();
    await this.page.keyboard.type(code, { delay: 20 });
  }

  /* ── forced password change ──────────────────────────────────────── */

  get changePasswordHeading(): Locator {
    return this.page.getByRole("heading", { name: this.t("changePassword.title") });
  }

  /**
   * Completes the forced change. The three fields are addressed by their own
   * labels; `exact` matters because "New password" is a prefix of "Confirm
   * new password" in English.
   */
  async completeForcedPasswordChange(currentPassword: string, newPassword: string): Promise<void> {
    await this.page.getByLabel(this.t("changePassword.currentPasswordLabel"), { exact: true }).fill(currentPassword);
    await this.page.getByLabel(this.t("changePassword.newPasswordLabel"), { exact: true }).fill(newPassword);
    await this.page.getByLabel(this.t("changePassword.confirmPasswordLabel"), { exact: true }).fill(newPassword);
    await this.page.getByRole("button", { name: this.t("changePassword.submit") }).click();
  }

  get forcedChangeSignOutButton(): Locator {
    return this.page.getByRole("button", { name: this.t("changePassword.signOut") });
  }

  /* ── the signed-in shell ─────────────────────────────────────────── */

  /** Present only once a session exists — the reliable "signed in" signal. */
  get chatSidebar(): Locator {
    return this.page.getByRole("navigation", { name: this.t("chat.sidebar.label") });
  }

  get settingsButton(): Locator {
    return this.page.getByRole("button", { name: this.t("chat.footer.account") });
  }

  get settingsDialog(): Locator {
    return this.page.getByRole("dialog");
  }

  async openSettings(): Promise<Locator> {
    await this.settingsButton.click();
    const dialog = this.settingsDialog;
    await dialog.getByRole("heading", { name: this.t("settings.title") }).waitFor();
    return dialog;
  }

  async openSessions(): Promise<Locator> {
    const dialog = this.settingsDialog;
    await dialog.getByRole("button", { name: this.t("settings.security.manageSessions") }).click();
    return dialog.getByRole("list", { name: this.t("settings.sessions.title") });
  }

  /* ── password reset ──────────────────────────────────────────────── */

  get resetEmailField(): Locator {
    return this.page.getByLabel(this.t("resetRequest.emailLabel"));
  }

  get resetSubmitButton(): Locator {
    return this.page.getByRole("button", { name: this.t("resetRequest.submit") });
  }
}
