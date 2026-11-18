import { act } from "@testing-library/react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import type { FlagsOn } from "./config";
import { FlagsProvider, useFlag } from "./client";

function Gated({ flag }: { flag: string }) {
  return <p>{useFlag(flag) ? "on" : "off"}</p>;
}

function page(on: FlagsOn, flag: string) {
  return (
    <FlagsProvider on={on}>
      <Gated flag={flag} />
    </FlagsProvider>
  );
}

async function serverRenderThenHydrate(on: FlagsOn, flag: string) {
  const container = document.createElement("div");
  container.innerHTML = renderToString(page(on, flag));
  const serverHtml = container.innerHTML;
  const recoverableErrors: unknown[] = [];
  const consoleError = vi
    .spyOn(console, "error")
    .mockImplementation(() => undefined);
  await act(async () => {
    hydrateRoot(container, page(on, flag), {
      onRecoverableError: (error) => recoverableErrors.push(error),
    });
    await Promise.resolve();
  });
  const consoleErrors = consoleError.mock.calls;
  consoleError.mockRestore();
  return {
    serverHtml,
    hydratedHtml: container.innerHTML,
    recoverableErrors,
    consoleErrors,
  };
}

describe("useFlag", () => {
  it("is on in the server HTML and after hydration for a flag the server turned on", async () => {
    const result = await serverRenderThenHydrate(
      ["meeting-alerts", "new-search"],
      "new-search",
    );

    expect(result.serverHtml).toBe("<p>on</p>");
    expect(result.hydratedHtml).toBe("<p>on</p>");
    expect(result.recoverableErrors).toEqual([]);
    expect(result.consoleErrors).toEqual([]);
  });

  it("is off for a flag the server did not turn on", async () => {
    const result = await serverRenderThenHydrate(
      ["new-search"],
      "new-search-v2",
    );

    expect(result.serverHtml).toBe("<p>off</p>");
    expect(result.hydratedHtml).toBe("<p>off</p>");
  });

  it("is on for any flag when the server turned every flag on", async () => {
    const result = await serverRenderThenHydrate("all", "anything-at-all");

    expect(result.serverHtml).toBe("<p>on</p>");
  });

  it("is off outside a FlagsProvider", () => {
    expect(renderToString(<Gated flag="new-search" />)).toBe("<p>off</p>");
  });
});
