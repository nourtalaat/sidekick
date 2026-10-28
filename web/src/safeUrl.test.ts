import { describe, expect, it } from "vitest";
import { safeHttpUrl } from "./safeUrl";

describe("safeHttpUrl", () => {
  it.each([
    { url: "https://example.com/a?b=c", want: "https://example.com/a?b=c" },
    { url: "http://127.0.0.1:8080/x", want: "http://127.0.0.1:8080/x" },
    { url: "javascript:alert(1)", want: undefined },
    { url: "JaVaScRiPt:alert(1)", want: undefined },
    { url: "data:text/html,<script>alert(1)</script>", want: undefined },
    { url: "ftp://example.com/file", want: undefined },
    { url: "//example.com/a", want: undefined },
    { url: "/relative/path", want: undefined },
    { url: "https://", want: undefined },
    { url: "not a url", want: undefined },
    { url: "", want: undefined },
    { url: undefined, want: undefined },
  ])("$url gives $want", ({ url, want }) => {
    expect(safeHttpUrl(url)).toBe(want);
  });
});
