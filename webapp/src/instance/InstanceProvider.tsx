import { useEffect, useState } from "react";
import type { ReactNode } from "react";

import {
  FALLBACK_INSTANCE_INFO,
  fetchInstanceInfo,
  InstanceContext,
} from "./instanceInfo";
import type { InstanceState } from "./instanceInfo";

/**
 * Fetches the public instance document once and hands it to every screen that
 * needs instance policy: the password minimum on each password form, and
 * whether the sign-in screen may offer a reset link at all.
 */
export function InstanceProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<InstanceState>({
    info: FALLBACK_INSTANCE_INFO,
    loaded: false,
  });

  useEffect(() => {
    let live = true;
    void fetchInstanceInfo().then((info) => {
      if (live) {
        setState({ info, loaded: true });
      }
    });
    return () => {
      live = false;
    };
  }, []);

  return <InstanceContext value={state}>{children}</InstanceContext>;
}
