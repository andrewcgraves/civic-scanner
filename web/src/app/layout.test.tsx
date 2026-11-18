import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import RootLayout, { metadata } from "./layout";

describe("RootLayout", () => {
  it("wraps the page in an English document", () => {
    const doc = new DOMParser().parseFromString(
      renderToStaticMarkup(
        <RootLayout>
          <p>page content</p>
        </RootLayout>,
      ),
      "text/html",
    );

    expect(doc.documentElement.getAttribute("lang")).toBe("en");
    expect(doc.body.innerHTML).toBe("<p>page content</p>");
  });

  it("sets the site title", () => {
    expect(metadata.title).toBe("Civic Scanner");
  });
});
