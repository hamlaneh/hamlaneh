import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import "../../i18n";
import en from "../../locales/en/common.json";
import { ConnectionBanner } from "./ConnectionBanner";

// Restored here, not at the end of the case that installs them: a failure
// before that line would otherwise leave fake timers installed for the rest
// of the file.
afterEach(() => {
  vi.useRealTimers();
});

/**
 * The three connection states the design draws (chat-components -> connection)
 * plus the quiet state. All of them announce through `role="status"` and none
 * of them takes focus.
 */

describe("ConnectionBanner", () => {
  it("draws the reconnecting banner with the time of the last connection", () => {
    render(
      <ConnectionBanner
        connection={{ status: "reconnecting", lastConnectedAt: "2026-08-21T09:41:00.000Z" }}
        justReconnected={false}
        onSettled={() => undefined}
      />,
    );

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent(en.chat.connection.reconnecting);
    expect(status.textContent).toMatch(/\d{2}:\d{2}/);
  });

  it("counts the retry down rather than hiding the schedule", () => {
    render(
      <ConnectionBanner
        connection={{ status: "offline", retryInSeconds: 8, lastConnectedAt: null }}
        justReconnected={false}
        onSettled={() => undefined}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      en.chat.connection.offline.replace("{{seconds}}", "8"),
    );
  });

  it("dismisses the back-online banner after three seconds", () => {
    vi.useFakeTimers();
    const onSettled = vi.fn();
    render(
      <ConnectionBanner
        connection={{ status: "online" }}
        justReconnected
        onSettled={onSettled}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(en.chat.connection.backOnline);
    expect(onSettled).not.toHaveBeenCalled();

    vi.advanceTimersByTime(3000);
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it("says nothing at all while the connection is healthy", () => {
    render(
      <ConnectionBanner
        connection={{ status: "online" }}
        justReconnected={false}
        onSettled={() => undefined}
      />,
    );

    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });
});
