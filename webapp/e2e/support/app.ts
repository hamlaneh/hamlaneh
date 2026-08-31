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
import { expect, type Locator, type Page } from "@playwright/test";

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

  /**
   * Opens the app at `path` and waits for the sign-in screen.
   *
   * The path matters for the chat specs: the pre-authentication screens are
   * chosen by session state rather than by URL, so signing in at
   * `/c/{channelId}` mounts the shell straight into that conversation. Landing
   * on "/" instead would take whatever the sidebar happens to list first,
   * which is not a thing a test should depend on.
   */
  async gotoSignIn(path = "/"): Promise<void> {
    await this.page.goto(path);
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

  /* ── the conversation ────────────────────────────────────────────── */

  /**
   * The conversation region. `role="log"` is what MessageList declares, and
   * its accessible name carries the channel, so the role alone identifies it.
   */
  get messageLog(): Locator {
    return this.page.getByRole("log");
  }

  /**
   * Every message body in the open conversation, in reading order.
   *
   * A body is markdown rendered into paragraphs, so `paragraph` is the role
   * that reaches them — and reaching them as a list is what lets a spec assert
   * the ORDER of a reloaded history rather than merely its contents.
   */
  get messageBodies(): Locator {
    return this.messageLog.getByRole("paragraph");
  }

  /**
   * The composer, located by the send button inside it.
   *
   * Its own accessible name is "Message {{target}}" with the target wrapped in
   * Unicode bidi isolates (chat/format.ts), which no plain string a spec
   * writes can match; the send button's name is a bare key.
   */
  get composerForm(): Locator {
    return this.page
      .locator("form")
      .filter({ has: this.page.getByRole("button", { name: this.t("chat.composer.send") }) });
  }

  get composerField(): Locator {
    return this.composerForm.getByRole("textbox");
  }

  /**
   * Types a message, sends it with Enter as the hint row promises, and waits
   * for the server to have stored it.
   *
   * The wait is not padding. The composer renders optimistically, so the text
   * is on screen before the request has been made — and since the client
   * holds one send queue per channel, a second message typed straight after
   * the first is still behind it. A test that only waited for the text could
   * reload before anything had been sent and find an empty channel, which is
   * exactly what happened on CI. "Sent" has to mean sent for a helper with
   * this name.
   */
  async sendMessage(content: string): Promise<void> {
    const stored = this.page.waitForResponse(
      (response) =>
        /\/api\/v1\/channels\/[^/]+\/messages$/.test(new URL(response.url()).pathname) &&
        response.request().method() === "POST" &&
        response.ok(),
    );
    await this.composerField.fill(content);
    await this.composerField.press("Enter");
    await stored;
  }

  /**
   * Attaches one file through the paperclip, and waits for its upload to land.
   *
   * The picker is opened by clicking the control the design draws rather than
   * by setting the hidden input directly: a paperclip that stopped opening the
   * picker would still pass the second way, and that is the regression this
   * helper exists to catch. The wait is on the upload's own response, because
   * a file that is still in flight is deliberately not sendable.
   *
   * The response is CHECKED, not merely awaited. It used to be enough that one
   * arrived, which made a refused upload indistinguishable from a stored one:
   * the composer draws a tray card either way, so the spec sailed past the
   * refusal and died sixty seconds later waiting for a send that could never
   * happen, naming the composer instead of the upload. A helper that says
   * "attached" has to mean it, and a test that fails should fail where the
   * truth is — the same rule accounts.ts's `expectOk` follows.
   */
  async attachFile(file: { name: string; mimeType: string; buffer: Buffer }): Promise<void> {
    const uploaded = this.page.waitForResponse(
      (response) =>
        /\/api\/v1\/channels\/[^/]+\/files$/.test(new URL(response.url()).pathname) &&
        response.request().method() === "POST",
    );
    const chooser = this.page.waitForEvent("filechooser");
    await this.composerForm.getByRole("button", { name: this.t("chat.composer.attach") }).click();
    await (await chooser).setFiles([file]);
    const response = await uploaded;
    if (!response.ok()) {
      throw new Error(
        `file upload (${file.name}): ${String(response.status())} ${await response.text()}`,
      );
    }
  }

  /** Inserts a `<@{user_id}>` mention token through the composer's picker. */
  async mentionInComposer(displayName: string): Promise<void> {
    await this.composerForm
      .getByRole("button", { name: this.t("chat.composer.mention") })
      .click();
    await this.composerForm.getByRole("button", { name: displayName, exact: true }).click();
  }

  /** The channel or DM name in the header — "#slug", or the peer's name. */
  get channelHeading(): Locator {
    return this.page.getByRole("heading", { level: 1 });
  }

  /* ── calls ───────────────────────────────────────────────────────── */

  /**
   * Answers the recovery-key offer with "Not now".
   *
   * The offer is account-level and arrives at first MLS use (ADR 010), so
   * since ADR 011 made every conversation encrypted it greets every new
   * account in whichever conversation they open first.
   *
   * It used to float, and it lay across the call strip: with it on screen the
   * control that starts a call could not be clicked at all, and this helper
   * was the thing standing between the media specs and that defect. It no
   * longer floats — an undesigned surface is a banner in the document flow
   * (chat.css), and `e2e/layout/plumbing-overlap.layout.ts` holds that in a
   * browser on any host, Docker or no Docker. So this is now what it says it
   * is: getting a banner out of the way of a screenshot, not working around a
   * dead button. Nothing here depends on it any more, and a spec that wants
   * the offer on screen is free to leave it there.
   *
   * Deliberately strict: it waits for the offer instead of shrugging when it
   * is absent. Every conversation is encrypted and every spec account is new,
   * so the offer always comes — and an offer that stopped appearing would be a
   * change worth a red test rather than a silently skipped click.
   */
  async declineBackupOffer(): Promise<void> {
    await this.page
      .getByRole("region", { name: this.t("chat.e2ee.backup.offerTitle") })
      .getByRole("button", { name: this.t("chat.e2ee.backup.offerNotNow") })
      .click();
  }

  /**
   * Clicks the one control in the call strip and waits for the tiles.
   *
   * Located by role rather than by label on purpose: the same control reads
   * "Start a call" or "Join call" depending on whether this person's client
   * has heard about the call yet, and which of the two it says is not
   * something a spec about media should depend on. The participant list
   * exists only once the session is connected, which is what makes waiting
   * for it the right "the call is up" signal.
   *
   * The offer is dismissed here rather than in each call spec: it arrives in
   * every conversation now, and a call spec's screenshots and traces are
   * easier to read without a banner nobody in the test is answering. It is no
   * longer required for the click to land — see `declineBackupOffer`.
   *
   * The wait for the composer is the call's own precondition, borrowed. Every
   * conversation is encrypted (ADR 011) and a call in one is refused outright
   * until this device holds the group's key — "a call here is never placed
   * unencrypted", which is the design and not a bug to route around. The
   * composer is withheld by the very same condition (ChatShell:
   * `encryptionNotReady`), so an enabled message field is the on-screen fact
   * that says the click can land. Waiting for it here rather than in each
   * spec is where it belongs: every caller needs it, and a spec that forgets
   * does not fail at the click — it hangs on participants that were never
   * going to appear, naming the wrong thing sixty seconds later.
   *
   * Generous, because this waits on a group commit across two browsers and a
   * server under a loaded runner, not on a render.
   */
  async joinCall(): Promise<void> {
    await this.declineBackupOffer();
    await expect(
      this.composerField,
      "the conversation's encryption never became ready on this device, so no call could be placed",
    ).toBeEnabled({ timeout: 60_000 });
    await this.page
      .getByRole("region", { name: this.t("calls.strip.label") })
      .getByRole("button")
      .click();
    await this.callParticipants.waitFor();
  }

  /** The participant grid — present only while a call session is connected. */
  get callParticipants(): Locator {
    return this.page.getByRole("list", { name: this.t("calls.view.participants") });
  }

  /* ── the sidebar ─────────────────────────────────────────────────── */

  /** One conversation row, by the label it draws. */
  conversationRow(label: string): Locator {
    return this.chatSidebar.getByRole("link", { name: label });
  }

  /**
   * The account button in the sidebar footer. It carries the caller's own
   * presence line, which is the one user-visible fact that says the realtime
   * socket is up — "Online" is written from the connection state and nothing
   * else (ChatShell: `myPresence`). A realtime test waits on this before it
   * sends anything, so a pass cannot come from a well-timed history load.
   */
  get identityButton(): Locator {
    return this.chatSidebar.getByRole("button", { name: this.t("account.title") });
  }

  /**
   * Creates a channel through the sidebar's "+".
   *
   * The dialog is undesigned plumbing, so everything here is plain semantic
   * HTML: a labelled text field, a radio group, a submit button.
   */
  async createChannel(slug: string, kind: "public" | "private" = "public"): Promise<void> {
    await this.chatSidebar
      .getByRole("button", { name: this.t("chat.sidebar.createChannel") })
      .click();
    const dialog = this.page.getByRole("dialog", { name: this.t("chat.createChannel.title") });
    await dialog.getByLabel(this.t("chat.createChannel.nameLabel")).fill(slug);
    if (kind === "private") {
      await dialog.getByLabel(this.t("chat.createChannel.private")).check();
    }
    await dialog.getByRole("button", { name: this.t("chat.createChannel.submit") }).click();
  }

  /**
   * Opens the invite picker. The empty state's primary action and the channel
   * menu's entry carry the same label, so one locator reaches whichever of
   * them the open channel is showing.
   */
  async openInvite(): Promise<void> {
    await this.page.getByRole("button", { name: this.t("chat.empty.invite") }).click();
  }

  /**
   * Invites somebody into the open channel through the people picker, which
   * must already be open.
   *
   * `search` is the username: the directory filters over username and display
   * name alike, and the username is the value with no spaces in it.
   */
  async invitePerson(search: string, displayName: string): Promise<void> {
    await this.pickPerson(
      this.t("chat.empty.invite"),
      this.t("chat.people.invite"),
      search,
      displayName,
    );
  }

  /** Opens a direct message through the sidebar's "+" beside Direct messages. */
  async startDirectMessage(search: string, displayName: string): Promise<void> {
    await this.chatSidebar
      .getByRole("button", { name: this.t("chat.sidebar.newDirectMessage") })
      .click();
    await this.pickPerson(
      this.t("chat.sidebar.newDirectMessage"),
      this.t("chat.people.message"),
      search,
      displayName,
    );
  }

  /** The shared half of both pickers: type, wait for the row, act on it. */
  private async pickPerson(
    title: string,
    actionLabel: string,
    search: string,
    displayName: string,
  ): Promise<void> {
    const picker = this.page.getByRole("dialog", { name: title });
    await picker.getByLabel(this.t("chat.people.searchLabel")).fill(search);
    await picker.getByRole("button", { name: `${actionLabel}: ${displayName}`, exact: true }).click();
  }

  /* ── password reset ──────────────────────────────────────────────── */

  get resetEmailField(): Locator {
    return this.page.getByLabel(this.t("resetRequest.emailLabel"));
  }

  get resetSubmitButton(): Locator {
    return this.page.getByRole("button", { name: this.t("resetRequest.submit") });
  }
}
