import type { ParseKeys } from "i18next";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { retryAfterSeconds } from "../api/client";

/** From this wait up the notice counts whole minutes; below it, whole seconds. */
const SECONDS_PER_MINUTE = 60;

/**
 * One screen's rate-limit sentence family: the undated wording it falls back
 * to, and the two counted variants that replace it once the server has stated
 * a wait. Three keys rather than a prefix so a screen that borrows another's
 * counted copy has to say so at the call site.
 */
export interface RateLimitKeys {
  undated: ParseKeys;
  seconds: ParseKeys;
  minutes: ParseKeys;
}

export interface RateLimitNotice {
  /**
   * The sentence to show while the screen is rate limited: the wait the server
   * stated, counted down, or the undated wording when it stated none this
   * client would act on (see `retryAfterSeconds`).
   */
  message: string;
  /** Reads a 429's `Retry-After` and starts counting the wait down. */
  start: (response: Response) => void;
  /** Drops the wait early — whatever exit path the screen already offered. */
  clear: () => void;
}

/**
 * The countdown behind every rate-limit notice in the app. Every 429 the
 * contract describes carries `Retry-After` (spec: RateLimited), so a screen
 * that shows "a few minutes" is guessing at an answer it was handed.
 *
 * `onExpire` runs once the stated wait has passed, for the screen to lift its
 * own notice with; it is read through a ref, so it may be an inline closure
 * without restarting the timer on every render.
 */
export function useRateLimitNotice(
  keys: RateLimitKeys,
  onExpire: () => void,
): RateLimitNotice {
  const { t } = useTranslation();
  // When the server's stated wait runs out, and how much of it is left. Both
  // are null/0 whenever the 429 named no wait this client would act on.
  const [retryAt, setRetryAt] = useState<number | null>(null);
  const [remaining, setRemaining] = useState(0);
  const onExpireRef = useRef(onExpire);

  useEffect(() => {
    onExpireRef.current = onExpire;
  });

  /**
   * The countdown re-reads the clock instead of decrementing a counter: a
   * throttled background tab fires the interval late, and a count that drifts
   * with it would be the same guess this stopped making.
   */
  useEffect(() => {
    if (retryAt === null) {
      return undefined;
    }
    const timer = setInterval(() => {
      const left = Math.ceil((retryAt - Date.now()) / 1000);
      if (left > 0) {
        setRemaining(left);
        return;
      }
      // The wait the server named has passed: the notice goes with the
      // condition it described.
      setRetryAt(null);
      setRemaining(0);
      onExpireRef.current();
    }, 1000);
    return () => {
      clearInterval(timer);
    };
  }, [retryAt]);

  const start = useCallback((response: Response) => {
    const wait = retryAfterSeconds(response);
    setRetryAt(wait === null ? null : Date.now() + wait * 1000);
    setRemaining(wait ?? 0);
  }, []);

  const clear = useCallback(() => {
    setRetryAt(null);
    setRemaining(0);
  }, []);

  let message: string;
  if (remaining <= 0) {
    message = t(keys.undated);
  } else if (remaining < SECONDS_PER_MINUTE) {
    message = t(keys.seconds, { count: remaining });
  } else {
    // Rounded up: telling someone four minutes when four and a half are left
    // just buys them another refusal.
    message = t(keys.minutes, { count: Math.ceil(remaining / SECONDS_PER_MINUTE) });
  }

  return { message, start, clear };
}
