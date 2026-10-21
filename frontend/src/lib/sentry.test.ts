import { describe, expect, it } from 'vitest';
import { scrubEvent, sentryOptions } from "./sentry";

describe("sentryOptions", () => {
  it("stays disabled when no DSN is configured", () => {
    const options = sentryOptions(undefined);
    expect(options.enabled).toBe(false);
  });

  it("reports errors only: no PII, tracing, trace headers or breadcrumbs", () => {
    const options = sentryOptions("https://key@o0.ingest.sentry.io/1");
    expect(options.enabled).toBe(true);
    expect(options.sendDefaultPii).toBe(false);
    expect(options.tracesSampleRate).toBe(undefined);
    expect(options.tracePropagationTargets).toEqual([]);
    expect(options.maxBreadcrumbs).toBe(0);
    expect(options.beforeSendTransaction()).toBe(null);
  });
});

describe("scrubEvent", () => {
  it("drops request data, user, breadcrumbs and extra context", () => {
    const event = scrubEvent({
      message: "boom",
      request: { url: "https://booktrading.bookchaowalit.com/portfolio?token=secret", headers: { cookie: "a=b" } },
      user: { ip_address: "1.2.3.4", email: "x@example.com" },
      breadcrumbs: [{ message: "clicked" }],
      extra: { body: "secret" },
    });
    expect(event.message).toBe("boom");
    expect(event.request).toBe(undefined);
    expect(event.user).toBe(undefined);
    expect(event.breadcrumbs).toBe(undefined);
    expect(event.extra).toBe(undefined);
  });

  it("removes local variables from stack frames", () => {
    const event = scrubEvent({
      exception: {
        values: [{ type: "Error", stacktrace: { frames: [{ function: "f", vars: { password: "p" } }] } }],
      },
    });
    const frame = event.exception?.values?.[0]?.stacktrace?.frames?.[0];
    expect(frame?.function).toBe("f");
    expect(frame?.vars).toBe(undefined);
  });
});
