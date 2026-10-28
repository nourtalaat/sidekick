import { afterEach, describe, expect, it } from "vitest";
import { CSRF_HEADER, createApiClient, setCsrfToken } from "./client";

function recordingFetch() {
  const requests: Request[] = [];

  const fetch = async (request: Request) => {
    requests.push(request);

    return new Response(JSON.stringify({ kind: "user", id: "u1", name: "Ada", role: "viewer", csrfToken: "t" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };

  return { requests, fetch };
}

describe("the API client", () => {
  afterEach(() => {
    setCsrfToken(undefined);
  });

  it("reads /me from the API base path", async () => {
    const { requests, fetch } = recordingFetch();
    const client = createApiClient({ baseUrl: "http://sidekick.test/api/v1", fetch });

    const { data } = await client.GET("/me");

    expect(data?.role).toBe("viewer");
    expect(requests[0]?.url).toBe("http://sidekick.test/api/v1/me");
    expect(requests[0]?.headers.has(CSRF_HEADER)).toBe(false);
  });

  it("sends the CSRF token on requests that change something, not on reads", async () => {
    const { requests, fetch } = recordingFetch();
    const client = createApiClient({ baseUrl: "http://sidekick.test/api/v1", fetch });

    setCsrfToken("token-1");
    await client.GET("/me");
    await client.POST("/jobs/{id}/retry", { params: { path: { id: 1 } } });

    expect(requests[0]?.headers.get(CSRF_HEADER)).toBeNull();
    expect(requests[1]?.method).toBe("POST");
    expect(requests[1]?.headers.get(CSRF_HEADER)).toBe("token-1");
  });
});
