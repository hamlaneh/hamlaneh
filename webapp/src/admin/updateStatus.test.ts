import { describe, expect, it } from "vitest";

import type { UpdateStatus } from "./adminApi";
import { describeUpdate } from "./updateStatus";

/** A self-updating instance with nothing waiting: every case is a delta on this. */
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

function view(patch: Partial<UpdateStatus>) {
  return describeUpdate({ ...CURRENT, ...patch });
}

describe("what the instance can do about updates", () => {
  it("reads a listening host with a comparable version as self-updating", () => {
    expect(view({}).capability).toBe("selfUpdating");
  });

  it("says updates are managed elsewhere when nothing on the host is listening", () => {
    expect(view({ self_update_available: false }).capability).toBe("managedElsewhere");
  });

  it("says the install came from a checkout when its version cannot be compared", () => {
    expect(view({ updatable: false, installed_version: "dev" }).capability).toBe("notFromRelease");
  });

  it("puts 'managed elsewhere' ahead of 'built from a checkout' when both are true", () => {
    // The order is the decision, not the accident. On a host that updates this
    // instance some other way — an orchestrator, a distribution package —
    // "reinstall from a release" is the wrong instruction, because reinstalling
    // is not how that host gets updates. Who updates this instance is the only
    // useful answer when nothing here is listening.
    expect(view({ self_update_available: false, updatable: false }).capability).toBe(
      "managedElsewhere",
    );
  });
});

describe("what the last check found", () => {
  it("reports up to date when a check has run and found nothing newer", () => {
    expect(view({}).availability).toBe("upToDate");
  });

  it("refuses to claim up to date before anything has checked", () => {
    // "No newer release was found" from an instance that never looked is a
    // claim about the world it has no grounds for, and it is the one an
    // operator would act on by doing nothing.
    expect(view({ last_check_at: null }).availability).toBe("unchecked");
  });

  it("names a newer release the channel will apply", () => {
    expect(view({ available_version: "v1.4.3" }).availability).toBe("available");
  });

  it("keeps a release the channel will not apply out of the applicable state", () => {
    expect(
      view({ available_version: "v1.5.0", available_outside_channel: true }).availability,
    ).toBe("outsideChannel");
  });

  it("does not read the installed version back as an available update", () => {
    // The contract calls this "the newest release the last check saw", which on
    // a current instance is the one already running. Offering Apply for the
    // version in the field above it would be the panel arguing with itself.
    expect(view({ available_version: CURRENT.installed_version }).availability).toBe("upToDate");
  });

  it("ignores an outside-channel flag with no release behind it", () => {
    expect(view({ available_version: null, available_outside_channel: true }).availability).toBe(
      "upToDate",
    );
  });

  it("claims nothing about availability on an instance that cannot check", () => {
    // Both no-control capabilities: neither may say "no newer release was
    // found", because neither has anything that looks.
    expect(view({ self_update_available: false }).availability).toBeNull();
    expect(view({ updatable: false }).availability).toBeNull();
  });

  it("still names a release it saw, even where it cannot apply one", () => {
    // Suppressing a fact the host did report would be the opposite failure to
    // the one above: the sentence about capability explains why there is no
    // button, and the version is worth knowing regardless.
    expect(view({ updatable: false, available_version: "v1.4.3" }).availability).toBe("available");
  });
});

describe("what the last run did", () => {
  it("reports nothing for a host that has never run and one sitting idle", () => {
    expect(view({ state: "unknown" }).outcome).toBeNull();
    expect(view({ state: "idle" }).outcome).toBeNull();
  });

  it.each(["succeeded", "failed", "rolled_back", "refused"] as const)(
    "reports %s as its own outcome",
    (state) => {
      expect(view({ state }).outcome).toBe(state);
    },
  );

  it("keeps rolled_back out of the failure vocabulary", () => {
    // The whole point of the state existing: the instance is fine and the
    // release is not, so it must never collapse into "failed".
    expect(view({ state: "rolled_back" }).outcome).not.toBe("failed");
  });

  it("reports no outcome while a run is still in flight", () => {
    expect(view({ state: "requested" }).outcome).toBeNull();
    expect(view({ state: "running" }).outcome).toBeNull();
  });
});

describe("a run in flight", () => {
  it.each(["requested", "running"] as const)("carries %s through as itself", (state) => {
    expect(view({ state }).inFlight).toBe(state);
  });

  it.each(["idle", "succeeded", "failed", "rolled_back", "refused", "unknown"] as const)(
    "is over in %s",
    (state) => {
      expect(view({ state }).inFlight).toBeNull();
    },
  );
});
