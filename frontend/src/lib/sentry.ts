// Sentry settings shared by the browser, Node.js and Edge runtimes.
// Errors only: no tracing, no trace headers on outgoing requests, no replay,
// and nothing that can carry user input (URLs, query strings, headers, cookies, bodies, locals).

type Frame = { vars?: unknown; filename?: string; abs_path?: string };
type ScrubbableEvent = {
  message?: string;
  logentry?: { message?: string; params?: unknown[] };
  fingerprint?: string[];
  request?: unknown;
  user?: unknown;
  breadcrumbs?: unknown;
  extra?: unknown;
  transaction?: string;
  tags?: Record<string, unknown>;
  contexts?: Record<string, unknown>;
  exception?: { values?: { type?: string; value?: string; stacktrace?: { frames?: Frame[] } }[] };
  debug_meta?: { images?: { code_file?: string }[] };
};

// Contexts that describe the platform only. Everything else (for example `nextjs`, which
// onRequestError fills with request_path, or `trace`/`response`) is dropped.
const SAFE_CONTEXTS = ["os", "runtime", "browser", "device", "app"];

const stripQuery = (value: string) => value.split(/[?#]/)[0];
const OPENER_OF = new Map([[")", "("], ["]", "["], ["}", "{"], [">", "<"]]);
// Drops a ?query or #hash inside free text, e.g. an error message. One glued to a word (full URLs,
// absolute paths, bare words such as `api?token=x`, quoted or parenthesised queries) is cut up to the
// next whitespace, so no character of the value survives except closing brackets that close one opened
// earlier in the text, such as the ")" of "(see /p?x=1)". A `?key=value` or `#key=value` word goes
// whole. A lone "?" at the end of a word, or a "#5" after a space, is not a query and stays.
const stripUrlQueries = (text: string) => {
  const unclosed = new Map<string, number>();
  const track = (part: string) => {
    for (const char of part) {
      const opener = OPENER_OF.get(char);
      if (opener) unclosed.set(opener, Math.max(0, (unclosed.get(opener) ?? 0) - 1));
      else if ("([{<".includes(char)) unclosed.set(char, (unclosed.get(char) ?? 0) + 1);
    }
  };
  return text.replace(/\S+/g, (word) => {
    if (/^[?#][^=]*=/.test(word)) return "";
    const at = word.slice(1).search(/[?#]/) + 1;
    if (at === 0 || at === word.length - 1) {
      track(word);
      return word;
    }
    const head = word.slice(0, at);
    track(head);
    let end = word.length;
    while (end > at + 1) {
      const opener = OPENER_OF.get(word.charAt(end - 1));
      const open = opener ? (unclosed.get(opener) ?? 0) : 0;
      if (!opener || open === 0) break;
      unclosed.set(opener, open - 1);
      end -= 1;
    }
    return head + word.slice(end);
  });
};

export function scrubEvent<T extends ScrubbableEvent>(event: T): T {
  delete event.request;
  delete event.user;
  delete event.breadcrumbs;
  delete event.extra;
  if (event.contexts) {
    const kept: Record<string, unknown> = {};
    for (const key of SAFE_CONTEXTS) if (key in event.contexts) kept[key] = event.contexts[key];
    event.contexts = kept;
  }
  if (typeof event.transaction === "string") event.transaction = stripQuery(event.transaction);
  // A thrown string or captureMessage text is sent as event.message (and logentry) as well as
  // in exception.values, so every free-text copy is cleaned.
  if (typeof event.message === "string") event.message = stripUrlQueries(event.message);
  if (event.logentry) {
    if (typeof event.logentry.message === "string") event.logentry.message = stripUrlQueries(event.logentry.message);
    event.logentry.params = event.logentry.params?.map((param) => (typeof param === "string" ? stripUrlQueries(param) : param));
  }
  if (event.fingerprint) event.fingerprint = event.fingerprint.map((part) => (typeof part === "string" ? stripUrlQueries(part) : part));
  if (event.tags) {
    delete event.tags.url;
    for (const [key, value] of Object.entries(event.tags)) {
      if (typeof value === "string") event.tags[key] = stripQuery(value);
    }
  }
  for (const value of event.exception?.values ?? []) {
    if (typeof value.type === "string") value.type = stripUrlQueries(value.type);
    if (typeof value.value === "string") value.value = stripUrlQueries(value.value);
    for (const frame of value.stacktrace?.frames ?? []) {
      delete frame.vars;
      if (typeof frame.filename === "string") frame.filename = stripQuery(frame.filename);
      if (typeof frame.abs_path === "string") frame.abs_path = stripQuery(frame.abs_path);
    }
  }
  for (const image of event.debug_meta?.images ?? []) {
    if (typeof image.code_file === "string") image.code_file = stripQuery(image.code_file);
  }
  return event;
}

export function sentryOptions(dsn: string | undefined) {
  return {
    dsn,
    enabled: Boolean(dsn),
    sendDefaultPii: false,
    tracesSampleRate: undefined,
    tracePropagationTargets: [] as string[],
    maxBreadcrumbs: 0,
    beforeSend: scrubEvent,
    beforeSendTransaction: () => null,
  };
}
