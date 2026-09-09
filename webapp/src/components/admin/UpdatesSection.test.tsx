import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { RequestUpdateRequest, UpdateStatus } from "../../admin/adminApi";
// Initialises i18next, which App does transitively; rendering a component on
// its own does not, and an uninitialised t() returns the key.
import i18n from "../../i18n";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import { server } from "../../mocks/node";
import { formatActivationDate } from "../../settings/sessionTime";
import { UpdatesSection } from "./UpdatesSection";

const UPDATES = en.admin.org.updates;

/** A listening host, a comparable version, and nothing waiting. */
const CURRENT: UpdateStatus = {
  installed_version: "v1.4.2",
  updatable: true,
  self_update_available: true,
  channel: "security",
  state: "idle",
  available_version: null,
  available_outside_channel: false,
  last_check_at: "2026-09-08T04:00:00Z",
  last_run_at: null,
  message: null,
};

/** What the host reports, replaced per case and mutated by the POST handler. */
let live: UpdateStatus;

/** Every body the panel POSTed, in order. */
let asked: RequestUpdateRequest[];

/** How the POST answers; `ok` writes the request and answers 202. */
let postAnswer: "ok" | 409 | 429 | 503 = "ok";

/** Seconds in the 429's `Retry-After`; null sends no header at all. */
let retryAfter: number | null = null;

/** Every GET the panel issued, counted so the poll can be observed. */
let reads: number;

function installHandlers() {
  server.use(
    http.get("/api/v1/admin/update", () => {
      reads += 1;
      return HttpResponse.json(live);
    }),
    http.post<never, RequestUpdateRequest>("/api/v1/admin/update", async ({ request }) => {
      asked = [...asked, await request.json()];
      if (postAnswer === 409) {
        return HttpResponse.json(
          { error: { code: "update_in_progress", message: "a run is already in flight" } },
          { status: 409 },
        );
      }
      if (postAnswer === 429) {
        return HttpResponse.json(
          { error: { code: "rate_limited", message: "too many requests" } },
          {
            status: 429,
            ...(retryAfter === null
              ? {}
              : { headers: { "Retry-After": String(retryAfter) } }),
          },
        );
      }
      if (postAnswer === 503) {
        return HttpResponse.json(
          { error: { code: "self_update_unavailable", message: "nothing is listening" } },
          { status: 503 },
        );
      }
      // 202 with the state the request produced: `requested`, which is what
      // starts the panel polling.
      live = { ...live, state: "requested" };
      return HttpResponse.json(live, { status: 202 });
    }),
  );
}

function renderPanel(status: Partial<UpdateStatus> = {}) {
  live = { ...CURRENT, ...status };
  render(<UpdatesSection />);
}

function checkButton() {
  return screen.getByRole("button", { name: UPDATES.check });
}

function queryApplyButton() {
  return screen.queryByRole("button", { name: UPDATES.apply });
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  live = { ...CURRENT };
  asked = [];
  postAnswer = "ok";
  retryAfter = null;
  reads = 0;
  installHandlers();
});

afterEach(async () => {
  server.resetHandlers();
  vi.useRealTimers();
  // The Persian case changes it, and every other case reads `en` strings.
  await i18n.changeLanguage("en");
});

afterAll(() => {
  server.close();
});

describe("an instance that is up to date", () => {
  it("names the version it runs and offers a check, and nothing to apply", async () => {
    renderPanel();

    expect(await screen.findByText(CURRENT.installed_version)).toBeInTheDocument();
    expect(screen.getByText(UPDATES.availability.upToDate)).toBeInTheDocument();
    expect(checkButton()).toBeEnabled();
    // Nothing is waiting, so there is nothing to apply. An Apply button that
    // applies the running version is the panel arguing with itself.
    expect(queryApplyButton()).toBeNull();
  });

  it("says which releases this host takes at all", async () => {
    renderPanel();

    // The frame for every sentence below it: "outside the channel" means
    // nothing without it.
    expect(await screen.findByText(UPDATES.channel.security)).toBeInTheDocument();
  });

  it("asks the host to check, and carries a kind and nothing else", async () => {
    const user = userEvent.setup({ delay: null });
    renderPanel();

    await user.click(await screen.findByRole("button", { name: UPDATES.check }));

    await waitFor(() => {
      expect(asked).toEqual([{ kind: "check" }]);
    });
    // No version, no repository, no force — ADR 016 decision 1. A body that
    // could name a version would grow a force flag, and a force flag is a
    // downgrade to any signed release with a known vulnerability.
    expect(Object.keys(asked[0] ?? {})).toEqual(["kind"]);
  });

  it("says when it last looked, so 'up to date' is not read as live truth", async () => {
    renderPanel();

    // The server performs no check of its own and reaches no network: this
    // panel is as fresh as `last_check_at` says it is, and never fresher.
    // Formatted rather than spelled out, so the assertion does not depend on
    // the runner's time zone or on ICU's month abbreviation.
    const when = formatActivationDate(CURRENT.last_check_at ?? "", "en");
    expect(when).not.toBe("");
    expect(
      await screen.findByText(UPDATES.checkedAt.replace("{{when}}", when)),
    ).toBeInTheDocument();
  });

  it("says nothing about when it looked where it has no claim to qualify", async () => {
    // A date attached to no sentence is a fact attached to nothing.
    renderPanel({ self_update_available: false });

    await screen.findByText(UPDATES.capability.managedElsewhere);
    expect(screen.queryByText(/Last checked/u)).toBeNull();
  });

  it("drops a timestamp it cannot read rather than printing 'Last checked .'", async () => {
    renderPanel({ last_check_at: "not-a-date" });

    await screen.findByText(UPDATES.availability.upToDate);
    expect(screen.queryByText(/Last checked/u)).toBeNull();
  });

  it("refuses to claim it is current before anything has checked", async () => {
    renderPanel({ last_check_at: null });

    expect(await screen.findByText(UPDATES.availability.unchecked)).toBeInTheDocument();
    expect(screen.queryByText(UPDATES.availability.upToDate)).toBeNull();
    // The check is still the way out of not knowing.
    expect(checkButton()).toBeEnabled();
  });
});

describe("an update the channel will apply", () => {
  it("names the version and offers Apply", async () => {
    renderPanel({ available_version: "v1.4.3" });

    expect(
      await screen.findByText(UPDATES.availability.available.replace("{{version}}", "v1.4.3")),
    ).toBeInTheDocument();
    expect(queryApplyButton()).toBeEnabled();
  });

  it("asks the host to apply, and carries a kind and nothing else", async () => {
    const user = userEvent.setup({ delay: null });
    renderPanel({ available_version: "v1.4.3" });

    await user.click(await screen.findByRole("button", { name: UPDATES.apply }));

    await waitFor(() => {
      expect(asked).toEqual([{ kind: "apply" }]);
    });
    expect(Object.keys(asked[0] ?? {})).toEqual(["kind"]);
  });
});

describe("an update outside the channel", () => {
  it("says a newer release exists and offers no way to apply it from here", async () => {
    renderPanel({ available_version: "v1.5.0", available_outside_channel: true });

    expect(
      await screen.findByText(
        UPDATES.availability.outsideChannel.replace("{{version}}", "v1.5.0"),
      ),
    ).toBeInTheDocument();
    // Applying it is a deliberate act on the host, not a click here. A button
    // that quietly did nothing would be worse than the absence.
    expect(queryApplyButton()).toBeNull();
    // Checking again is still honest: it is what would find the next patch.
    expect(checkButton()).toBeEnabled();
  });
});

describe("a run in flight", () => {
  it("disables the controls and says what is happening", async () => {
    renderPanel({ state: "running", available_version: "v1.4.3" });

    expect(await screen.findByText(UPDATES.progress.running)).toBeInTheDocument();
    expect(checkButton()).toBeDisabled();
    expect(queryApplyButton()).toBeDisabled();
  });

  it("distinguishes a request the host has not picked up from work in progress", async () => {
    renderPanel({ state: "requested" });

    expect(await screen.findByText(UPDATES.progress.requested)).toBeInTheDocument();
    expect(screen.queryByText(UPDATES.progress.running)).toBeNull();
  });

  it("re-reads the status while the run lasts, and stops once it settles", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderPanel({ state: "running" });

    await waitFor(() => {
      expect(screen.getByText(UPDATES.progress.running)).toBeInTheDocument();
    });
    const afterFirstRead = reads;

    // The host finishes; the next poll brings the outcome back.
    live = { ...live, state: "succeeded", last_run_at: "2026-09-08T05:00:00Z" };
    await vi.advanceTimersByTimeAsync(3000);
    await waitFor(() => {
      expect(screen.getByText(UPDATES.outcome.succeeded)).toBeInTheDocument();
    });
    const afterSettling = reads;
    expect(afterSettling).toBeGreaterThan(afterFirstRead);

    // And then it stops: a settled panel that keeps polling is a request every
    // three seconds for as long as the tab is open.
    await vi.advanceTimersByTimeAsync(12000);
    expect(reads).toBe(afterSettling);
  });

  it("starts polling off the 202, without waiting for the operator to reload", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ delay: null, advanceTimers: vi.advanceTimersByTime });
    renderPanel({ available_version: "v1.4.3" });

    await user.click(await screen.findByRole("button", { name: UPDATES.apply }));

    // The 202 carries `requested`, and that is what the panel draws.
    expect(await screen.findByText(UPDATES.progress.requested)).toBeInTheDocument();
    const afterRequest = reads;
    await vi.advanceTimersByTimeAsync(3000);
    await waitFor(() => {
      expect(reads).toBeGreaterThan(afterRequest);
    });
  });

  it("keeps polling when a poll fails, because that is what a restart looks like", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderPanel({ state: "running" });

    await waitFor(() => {
      expect(screen.getByText(UPDATES.progress.running)).toBeInTheDocument();
    });

    // The server goes away mid-update: exactly what an applied release does.
    server.use(
      http.get("/api/v1/admin/update", () => {
        reads += 1;
        return HttpResponse.json(
          { error: { code: "internal_error", message: "restarting" } },
          { status: 502 },
        );
      }),
    );
    const beforeOutage = reads;
    await vi.advanceTimersByTimeAsync(3000);
    await waitFor(() => {
      expect(reads).toBeGreaterThan(beforeOutage);
    });
    // Still the running panel, not a load failure: the instance came back is
    // the expected next event, and the panel has to be there to see it.
    expect(screen.getByText(UPDATES.progress.running)).toBeInTheDocument();

    // And it is: the next poll after it returns carries the outcome.
    live = { ...live, state: "succeeded" };
    installHandlers();
    await vi.advanceTimersByTimeAsync(3000);
    expect(await screen.findByText(UPDATES.outcome.succeeded)).toBeInTheDocument();
  });
});

describe("how a finished run reads", () => {
  it("says a rollback left a working instance, never a broken one", async () => {
    renderPanel({ state: "rolled_back", message: "health check failed after 3 attempts" });

    // The state exists so that an operator does not go looking for a broken
    // instance: the release was not healthy, and the previous one is serving.
    expect(await screen.findByText(UPDATES.outcome.rolled_back)).toBeInTheDocument();
    expect(screen.queryByText(UPDATES.outcome.failed)).toBeNull();
  });

  it("says a refusal was deliberate", async () => {
    renderPanel({ state: "refused" });

    expect(await screen.findByText(UPDATES.outcome.refused)).toBeInTheDocument();
  });

  it("reports a failure without claiming the instance is unusable", async () => {
    renderPanel({ state: "failed" });

    expect(await screen.findByText(UPDATES.outcome.failed)).toBeInTheDocument();
    // The controls come back: a failed run is a run to retry, not a dead end.
    expect(checkButton()).toBeEnabled();
  });

  it.each(["succeeded", "failed", "rolled_back", "refused"] as const)(
    "renders the updater's own line as preformatted text in %s",
    async (state) => {
      const line = 'ERROR: <img src=x onerror="alert(1)"> exit status 1';
      renderPanel({ state, message: line });

      const reported = await screen.findByText(line);
      // A <pre> and never markup: it is a host-generated string on the most
      // authenticated surface in the product.
      expect(reported.tagName).toBe("PRE");
      expect(reported.querySelector("img")).toBeNull();
      expect(reported.textContent).toBe(line);
    },
  );

  it("draws no report block when the host wrote no message", async () => {
    renderPanel({ state: "succeeded", message: null });

    await screen.findByText(UPDATES.outcome.succeeded);
    expect(screen.queryByText(UPDATES.messageLabel)).toBeNull();
  });
});

describe("an instance that cannot update itself", () => {
  it("says updates are managed elsewhere, and draws no button, when nothing is listening", async () => {
    renderPanel({ self_update_available: false });

    expect(await screen.findByText(UPDATES.capability.managedElsewhere)).toBeInTheDocument();
    // An offer the instance cannot honour would leave an operator believing
    // they are patched (ADR 016 decision 3).
    expect(screen.queryByRole("button", { name: UPDATES.check })).toBeNull();
    expect(queryApplyButton()).toBeNull();
  });

  it("says the install came from a checkout, and draws no button, when it cannot be compared", async () => {
    renderPanel({ updatable: false, installed_version: "dev" });

    expect(await screen.findByText(UPDATES.capability.notFromRelease)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: UPDATES.check })).toBeNull();
    expect(queryApplyButton()).toBeNull();
    // The version is still reported: it is the reason for the sentence.
    expect(screen.getByText("dev")).toBeInTheDocument();
  });

  it("claims nothing about being up to date on an instance that cannot check", async () => {
    renderPanel({ self_update_available: false });

    await screen.findByText(UPDATES.capability.managedElsewhere);
    expect(screen.queryByText(UPDATES.availability.upToDate)).toBeNull();
    expect(screen.queryByText(UPDATES.availability.unchecked)).toBeNull();
  });
});

describe("a refused request", () => {
  it("counts down the wait the server named, rather than guessing at it", async () => {
    const user = userEvent.setup({ delay: null });
    postAnswer = 429;
    retryAfter = 90;
    renderPanel();

    await user.click(await screen.findByRole("button", { name: UPDATES.check }));

    // 90 seconds is two whole minutes rounded up: telling somebody one minute
    // when ninety seconds are left just buys them another refusal.
    expect(
      await screen.findByText(
        en.resetRequest.error.rateLimitedMinutes_other.replace("{{count, number}}", "2"),
      ),
    ).toBeInTheDocument();
  });

  it("falls back to the undated sentence when the server named no wait", async () => {
    const user = userEvent.setup({ delay: null });
    postAnswer = 429;
    renderPanel();

    await user.click(await screen.findByRole("button", { name: UPDATES.check }));

    expect(await screen.findByText(UPDATES.error.rateLimited)).toBeInTheDocument();
  });

  it("says a run is already in flight, and re-reads what the host now reports", async () => {
    const user = userEvent.setup({ delay: null });
    postAnswer = 409;
    renderPanel();

    await screen.findByText(UPDATES.availability.upToDate);
    // The scheduled timer took the host-wide lock between the panel loading
    // and the click.
    live = { ...live, state: "running" };
    await user.click(checkButton());

    expect(await screen.findByText(UPDATES.error.inProgress)).toBeInTheDocument();
    // And the panel redraws as the run it collided with, rather than leaving an
    // error line over a picture that says nothing is happening.
    expect(await screen.findByText(UPDATES.progress.running)).toBeInTheDocument();
  });

  it("says the request was never written when nothing is listening any more", async () => {
    const user = userEvent.setup({ delay: null });
    postAnswer = 503;
    renderPanel();

    await screen.findByText(UPDATES.availability.upToDate);
    // The watcher went away while this screen was open; the GET now agrees.
    live = { ...live, self_update_available: false };
    await user.click(checkButton());

    expect(await screen.findByText(UPDATES.error.unavailable)).toBeInTheDocument();
    expect(await screen.findByText(UPDATES.capability.managedElsewhere)).toBeInTheDocument();
  });
});

describe("when the status cannot be read at all", () => {
  it("fails inside its own panel and offers to try again", async () => {
    const user = userEvent.setup({ delay: null });
    server.use(
      http.get("/api/v1/admin/update", () => {
        reads += 1;
        return reads === 1
          ? HttpResponse.json(
              { error: { code: "internal_error", message: "no state directory" } },
              { status: 500 },
            )
          : HttpResponse.json(live);
      }),
    );
    render(<UpdatesSection />);

    expect(await screen.findByText(UPDATES.loadFailed)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: en.admin.error.retry }));

    expect(await screen.findByText(UPDATES.availability.upToDate)).toBeInTheDocument();
  });
});

describe("in Persian", () => {
  it("keeps the version in Latin digits and pinned left to right", async () => {
    await i18n.changeLanguage("fa");
    renderPanel();

    const version = await screen.findByText(CURRENT.installed_version);
    // A version is read character by character; reordering it in an RTL
    // interface would make v1.4.2 and 2.4.1v indistinguishable at a glance.
    expect(version).toHaveAttribute("dir", "ltr");
    expect(screen.getByText(fa.admin.org.updates.availability.upToDate)).toBeInTheDocument();
  });

  it("pins the updater's own line left to right too", async () => {
    await i18n.changeLanguage("fa");
    const line = "docker compose pull: exit status 1";
    renderPanel({ state: "failed", message: line });

    expect(await screen.findByText(line)).toHaveAttribute("dir", "ltr");
  });
});
