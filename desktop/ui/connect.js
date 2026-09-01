/**
 * The desktop shell's only local page: name an instance, then hand the window
 * to it.
 *
 * Everything past this point is the instance's own web application, served
 * over HTTPS from its own origin — the identical bundle a browser gets. That
 * is deliberate and not a shortcut. `webapp/src/api/client.ts` sets its base
 * URL to `window.location.origin` and the session travels in HttpOnly cookies
 * with a double-submit CSRF cookie beside it, so a copy of the SPA bundled
 * into the app would run on `tauri://localhost`, an origin with no server
 * behind it and no way to hold that instance's cookies. Loading the real
 * origin keeps every one of those mechanisms exactly as tested.
 */

/** The stored address, so the field is prefilled on the next launch. */
const LAST_INSTANCE_KEY = "hamlaneh.lastInstance";

/**
 * User-facing text, keyed the same way the web application keys its own —
 * no bare strings in the markup (CLAUDE.md "Language policy").
 *
 * It is a literal here rather than a read of `webapp/src/locales/**` because
 * this page ships with no bundler and no i18next: those files are TypeScript
 * module input, and pulling them in would mean giving the shell a build step
 * to serve six strings. `desktop/connect.test.js` asserts en/fa parity so the
 * two halves cannot drift apart unnoticed, which is the property
 * `npm run i18n:check` gives the web application.
 */
export const STRINGS = {
  en: {
    heading: "Connect to your Hamlaneh instance",
    label: "Instance address",
    hint: "For example https://chat.example.com, or http://localhost:8080 in home mode.",
    submit: "Connect",
    invalid: "Enter a full web address beginning with https:// or http://.",
  },
  fa: {
    heading: "به نمونهٔ هم‌لانهٔ خود متصل شوید",
    label: "نشانی نمونه",
    hint: "برای نمونه https://chat.example.com یا در حالت خانگی http://localhost:8080",
    submit: "اتصال",
    invalid: "یک نشانی وب کامل که با https:// یا http:// آغاز شود وارد کنید.",
  },
};

/** Falls back to English for every locale that is not Persian. */
export function pickLocale(languages) {
  return languages.some((tag) => tag.toLowerCase().startsWith("fa")) ? "fa" : "en";
}

/**
 * The origin to hand the window, or null when the text cannot name one.
 *
 * This is a trust boundary, not tidying: whatever it returns is passed
 * straight to `location.assign`, so a `javascript:` or `file:` address typed
 * into the field would otherwise run in the shell's own privileged origin.
 * Only http and https survive, and only the origin does — the web application
 * addresses its API from `window.location.origin`, so a path would be
 * discarded on the first navigation anyway.
 */
export function instanceOrigin(raw) {
  const text = raw.trim();
  if (text === "") {
    return null;
  }
  // A bare host is what people type. Assume https rather than refuse, but
  // only when no scheme was given at all — `javascript:...` matches here and
  // is rejected below, on its protocol, rather than being silently prefixed.
  const hasScheme = /^[a-z][a-z0-9+.-]*:/iu.test(text);
  let url;
  try {
    url = new URL(hasScheme ? text : `https://${text}`);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    return null;
  }
  if (url.hostname === "") {
    return null;
  }
  return url.origin;
}

// The DOM half. Guarded so `node --test` can import the exports above without
// a document; there is no other reason for the condition.
if (typeof document !== "undefined") {
  const strings = STRINGS[pickLocale([...navigator.languages])];
  const root = document.documentElement;
  root.lang = pickLocale([...navigator.languages]);
  root.dir = root.lang === "fa" ? "rtl" : "ltr";

  for (const node of document.querySelectorAll("[data-string]")) {
    node.textContent = strings[node.dataset.string];
  }

  const form = document.querySelector("form");
  const field = document.querySelector("#instance");
  const error = document.querySelector("#error");

  // ponytail: a wrong address strands the window on the browser's own error
  // page with no way back to this one — restarting the app is the way out.
  // A "return to connect" control needs either a native menu item or an
  // on_navigation handler in Rust; add one when someone hits it for real.
  field.value = localStorage.getItem(LAST_INSTANCE_KEY) ?? "";

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const origin = instanceOrigin(field.value);
    if (origin === null) {
      error.textContent = strings.invalid;
      field.setAttribute("aria-invalid", "true");
      field.focus();
      return;
    }
    localStorage.setItem(LAST_INSTANCE_KEY, origin);
    location.assign(origin);
  });
}
