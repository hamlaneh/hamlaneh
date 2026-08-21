import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ErrorBoundary } from "./ErrorBoundary";

/**
 * Without a boundary, one thrown render unmounts the whole React root and
 * leaves a blank page — which a crafted link is enough to trigger. These
 * assert that the failure stays a message.
 */

function Boom(): never {
  throw new Error("render exploded");
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("ErrorBoundary", () => {
  it("renders its children while nothing has thrown", () => {
    render(
      <ErrorBoundary fallback={<p>fallback</p>}>
        <p>the app</p>
      </ErrorBoundary>,
    );

    expect(screen.getByText("the app")).toBeInTheDocument();
    expect(screen.queryByText("fallback")).not.toBeInTheDocument();
  });

  it("shows the recovery message instead of a blank page when a child throws", () => {
    // React logs the caught error itself; silenced so the run stays readable.
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);

    render(
      <ErrorBoundary fallback={<p>fallback</p>}>
        <Boom />
      </ErrorBoundary>,
    );

    expect(screen.getByText("fallback")).toBeInTheDocument();
    expect(consoleError).toHaveBeenCalled();
  });

  it("never puts the error text on the page", () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);

    render(
      <ErrorBoundary fallback={<p>fallback</p>}>
        <Boom />
      </ErrorBoundary>,
    );

    expect(document.body.textContent).not.toContain("render exploded");
  });
});
