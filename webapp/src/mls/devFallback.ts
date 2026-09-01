import { fakeMlsModule, fakeWorld } from "./fakeModule";
import type { MlsDeviceHandle } from "./wasm";

/**
 * Stands in for the compiled MLS wrapper when `VITE_API_MOCK=1 npm run dev` is
 * pointed at a checkout where `src-mls/build.sh` has never run.
 *
 * WHY IT EXISTS: `src/mls/wasm.ts` imports the `hamlaneh-mls` alias, and Vite
 * resolves every import specifier — dynamic ones included — while it transforms
 * a module. With no `src-mls/pkg/`, that transform is a 500 and the whole
 * signed-in app fails to load, which is why backend-less dev could not reach a
 * single encrypted screen. On a strict-mode instance that is every screen.
 *
 * WHY IT IS SAFE: vite.config.ts substitutes this file only when the dev
 * server is running AND `VITE_API_MOCK=1` AND the real artifact is genuinely
 * absent. A production build always resolves the alias to the compiled crate
 * and fails loudly if it is missing — no build can ever pick this up, and a
 * dev server without the mock flag cannot either.
 *
 * It performs no cryptography and pretends to none: it is the same double
 * `src/mls/service.test.ts` drives, so anything it renders is a statement
 * about layout and plumbing, never about confidentiality.
 */
const module = fakeMlsModule(fakeWorld());

export const MlsDevice = {
  create: (identity: string): MlsDeviceHandle => module.create(identity),
  restore: (state: Uint8Array): MlsDeviceHandle => module.restore(state),
};
