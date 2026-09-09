/**
 * What the Updates panel says, derived from one `UpdateStatus` (ADR 016).
 *
 * A pure function rather than conditionals spread through the component,
 * because the interesting part is not the markup — it is which fact outranks
 * which, and those rules are silent until they are wrong on somebody's server.
 * Every precedence below is pinned by a test.
 */
import type { UpdateStatus } from "./adminApi";

/** Whether this instance manages its own updates at all, and if not, why not. */
export type UpdateCapability =
  /** Nothing on the host is listening, so the panel reports and does not act. */
  | "managedElsewhere"
  /** The binary reports something that is not a version, so it cannot be updated in place. */
  | "notFromRelease"
  /** A watcher is installed and the version is comparable: the buttons are real. */
  | "selfUpdating";

/** What the last check found — or that nothing has checked. */
export type UpdateAvailability =
  | "unchecked"
  | "upToDate"
  | "available"
  /** A newer release the configured channel will not apply. Never an Apply button. */
  | "outsideChannel";

/** What the last run did, when that is worth a sentence of its own. */
export type UpdateOutcome = "succeeded" | "failed" | "rolled_back" | "refused";

export interface UpdateView {
  capability: UpdateCapability;
  /**
   * Null only when this instance manages no updates and has seen no release:
   * an instance that cannot check has no grounds to claim it is up to date,
   * and saying so anyway is the sentence an operator would act on.
   */
  availability: UpdateAvailability | null;
  outcome: UpdateOutcome | null;
  /**
   * Non-null while a run is in flight: every control is disabled and the panel
   * polls the GET. Carries which of the two in-flight states it is, rather than
   * a boolean, because they say different things — one is a request the host
   * has not picked up, the other is work already happening.
   */
  inFlight: UpdateInFlight | null;
}

/** The two states that mean a run is in flight rather than finished. */
export type UpdateInFlight = "requested" | "running";

function inFlightOf(state: UpdateStatus["state"]): UpdateInFlight | null {
  return state === "requested" || state === "running" ? state : null;
}

/** A switch rather than a set, so a state added to the contract fails to compile. */
function outcomeOf(state: UpdateStatus["state"]): UpdateOutcome | null {
  switch (state) {
    case "succeeded":
    case "failed":
    case "rolled_back":
    case "refused":
      return state;
    case "idle":
    case "requested":
    case "running":
    case "unknown":
      // None of these describe a finished run, so none has an outcome to report.
      return null;
  }
}

/**
 * `self_update_available` is read before `updatable`, and the order is a
 * decision rather than an accident: on a host that manages updates elsewhere —
 * an orchestrator, a distribution package — "reinstall from a release" is
 * wrong advice, because reinstalling is not how that host gets updates. Where
 * nothing here is listening, who updates this instance is the only useful
 * answer, and the version's shape is an internal detail of an updater that is
 * not going to run.
 */
function capabilityOf(status: UpdateStatus): UpdateCapability {
  if (!status.self_update_available) {
    return "managedElsewhere";
  }
  return status.updatable ? "selfUpdating" : "notFromRelease";
}

function availabilityOf(
  status: UpdateStatus,
  capability: UpdateCapability,
): UpdateAvailability | null {
  const offered = status.available_version ?? "";
  // The contract says `available_version` is "the newest release the last
  // check saw", which on a current instance is the one already installed. That
  // is not an update, and drawing it as one would offer Apply for the version
  // already running.
  if (offered !== "" && offered !== status.installed_version) {
    return status.available_outside_channel === true ? "outsideChannel" : "available";
  }
  if (capability !== "selfUpdating") {
    // Nothing checks on this host, so there is no check to report. "Up to
    // date" here would be a claim about the world made by an instance that
    // never looked at it.
    return null;
  }
  return status.last_check_at === null || status.last_check_at === undefined
    ? "unchecked"
    : "upToDate";
}

export function describeUpdate(status: UpdateStatus): UpdateView {
  const capability = capabilityOf(status);
  return {
    capability,
    availability: availabilityOf(status, capability),
    outcome: outcomeOf(status.state),
    inFlight: inFlightOf(status.state),
  };
}
