import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

// Initialises i18next, which App does transitively; rendering a component
// on its own does not, and an uninitialised t() returns the key.
import "../../i18n";
import en from "../../locales/en/common.json";
import { FIXTURE_ADMIN } from "../../mocks/handlers";
import { AdminShell } from "./AdminShell";

/**
 * The nav counts, which shipped wrong.
 *
 * `counts` used to be `{users?, invites?}` read through a ternary that named
 * `users` and let everything else fall through to `invites` — so the invites
 * screen, which passes `{invites: 0}`, drew a `0` beside "Invitations",
 * "Organisation settings" AND "Audit log". It survived two phases because
 * /admin 404'd on navigation and admin.css was never imported, so nobody
 * reached the screen that shows it.
 */
function renderShell(counts?: Parameters<typeof AdminShell>[0]["counts"]) {
  render(
    <MemoryRouter initialEntries={["/admin/invites"]}>
      <AdminShell
        currentUser={FIXTURE_ADMIN}
        organizationName="Hamlaneh"
        title={en.admin.invites.title}
        subtitle={en.admin.invites.subtitle}
        {...(counts === undefined ? {} : { counts })}
      >
        <p>pane</p>
      </AdminShell>
    </MemoryRouter>,
  );
  return screen.getByRole("navigation", { name: en.admin.nav.label });
}

/** The count rendered beside one nav row, or null when it has none. */
function countBeside(nav: HTMLElement, label: string): string | null {
  const row = within(nav).getByRole("link", { name: new RegExp(label) });
  return row.querySelector(".hm-admin__nav-count")?.textContent ?? null;
}

describe("the admin nav", () => {
  it("shows a count only on the row it was given one for", () => {
    const nav = renderShell({ invites: 0 });

    expect(countBeside(nav, en.admin.nav.invites)).toBe("0");
    expect(countBeside(nav, en.admin.nav.users)).toBeNull();
    expect(countBeside(nav, en.admin.nav.org)).toBeNull();
    expect(countBeside(nav, en.admin.nav.audit)).toBeNull();
  });

  it("puts the users count on the users row", () => {
    const nav = renderShell({ users: 7 });

    expect(countBeside(nav, en.admin.nav.users)).toBe("7");
    expect(countBeside(nav, en.admin.nav.invites)).toBeNull();
  });

  it("shows no counts at all when none are supplied", () => {
    const nav = renderShell();

    expect(nav.querySelectorAll(".hm-admin__nav-count")).toHaveLength(0);
  });
});
