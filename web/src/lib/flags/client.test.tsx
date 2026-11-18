import { act } from "@testing-library/react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { describe, expect, expectTypeOf, it, vi } from "vitest";

import { FlagsProvider, useFlag } from "./client";
import type { FlagKey } from "./registry";

const REGISTERED: FlagKey = "test-only-flag";

function Gated({ flag }: { flag: FlagKey }) {
  return <p>{useFlag(flag) ? "on" : "off"}</p>;
}

function page(on: readonly FlagKey[], flag: FlagKey) {
  return (
    <FlagsProvider on={on}>
      <Gated flag={flag} />
    </FlagsProvider>
  );
}

async function serverRenderThenHydrate(on: readonly FlagKey[], flag: FlagKey) {
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
  it("accepts only registered flag keys", () => {
    expectTypeOf(useFlag).parameter(0).toEqualTypeOf<FlagKey>();
    expectTypeOf<"not-registered">().not.toExtend<FlagKey>();
  });

  it("is on in the server HTML and after hydration for a flag the server turned on", async () => {
    const result = await serverRenderThenHydrate([REGISTERED], REGISTERED);

    expect(result.serverHtml).toBe("<p>on</p>");
    expect(result.hydratedHtml).toBe("<p>on</p>");
    expect(result.recoverableErrors).toEqual([]);
    expect(result.consoleErrors).toEqual([]);
  });

  it("is off for a flag the server did not turn on", async () => {
    const result = await serverRenderThenHydrate([], REGISTERED);

    expect(result.serverHtml).toBe("<p>off</p>");
    expect(result.hydratedHtml).toBe("<p>off</p>");
  });

  it("is off for a flag that is not registered", () => {
    const unregistered = "not-registered" as FlagKey;

    expect(renderToString(page([REGISTERED], unregistered))).toBe("<p>off</p>");
  });

  it("is off outside a FlagsProvider", () => {
    expect(renderToString(<Gated flag={REGISTERED} />)).toBe("<p>off</p>");
  });
});
