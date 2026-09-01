/**
 * The dashboard's half of the admin contract (openapi.yaml, tag `admin`).
 *
 * Everything here throws `AdminError` on failure, carrying the server's own
 * stable error code so a screen can localize by code rather than render the
 * English message the contract sends for developers.
 */
import { api } from "../api/client";
import type { components } from "../api/schema";

type User = components["schemas"]["User"];
type AdminUserResponse = components["schemas"]["AdminUser"];
export type Invite = components["schemas"]["Invite"];
export type CreatedInvite = components["schemas"]["CreatedInvite"];
export type OrgSettings = components["schemas"]["OrgSettings"];
export type UpdateOrgSettingsRequest = components["schemas"]["UpdateOrgSettingsRequest"];
export type EncryptionMode = components["schemas"]["EncryptionMode"];
export type SetEncryptionModeRequest = components["schemas"]["SetEncryptionModeRequest"];
export type AuditEntry = components["schemas"]["AuditEntry"];
export type AuditPage = components["schemas"]["AuditPage"];
export type TemporaryCredentials = components["schemas"]["TemporaryCredentials"];
export type AdminCreateUserRequest = components["schemas"]["AdminCreateUserRequest"];
export type CreateInviteRequest = components["schemas"]["CreateInviteRequest"];
export type ScimToken = components["schemas"]["ScimToken"];
export type CreatedScimToken = components["schemas"]["CreatedScimToken"];
export type CreateScimTokenRequest = components["schemas"]["CreateScimTokenRequest"];

/**
 * A row of the users table.
 *
 * The contract types the LIST response as `User` and the PATCH response as
 * `AdminUser`, and only the second carries `is_active`. Rather than let the
 * table depend on which endpoint a row came from, both are normalised here
 * and a row with no `is_active` is read as active — which is what an account
 * the list endpoint returns has always been.
 *
 * CONTRACT GAP, reported rather than patched: `adminListUsers` should answer
 * `AdminUserPage` (rows of `AdminUser`), otherwise a deactivated account is
 * indistinguishable from an active one until somebody edits it.
 */
export interface UserRow {
  id: string;
  username: string;
  email: string | null;
  display_name: string;
  is_admin: boolean;
  is_active: boolean;
  must_change_password: boolean;
  created_at: string;
}

function toRow(user: User | AdminUserResponse): UserRow {
  return {
    id: user.id,
    username: user.username,
    email: user.email ?? null,
    display_name: user.display_name,
    is_admin: user.is_admin,
    is_active: "is_active" in user ? user.is_active : true,
    must_change_password: user.must_change_password,
    created_at: user.created_at,
  };
}

/** A refusal the screen must be able to tell apart, by the contract's code. */
export class AdminError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`admin request failed: ${String(status)} ${code}`);
    this.name = "AdminError";
  }
}

/** Turns openapi-fetch's `{ data, error, response }` into a value or a throw. */
function unwrap<T>(
  result: { data?: T; error?: components["schemas"]["Error"]; response: Response },
): T {
  if (result.data !== undefined) {
    return result.data;
  }
  throw new AdminError(result.response.status, result.error?.error.code ?? "unexpected");
}

/**
 * Pages to follow before giving up on reaching the end of a list.
 *
 * ponytail: a bounded walk, not a pager — the users and invites tables draw
 * no paging control and an instance's own account list is tens to hundreds
 * of rows. If a deployment ever passes 2000 accounts this needs the audit
 * log's older/newer control instead of a bigger cap.
 */
const MAX_PAGES = 20;
const PAGE_LIMIT = 100;

export async function listUsers(): Promise<UserRow[]> {
  const rows: UserRow[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page += 1) {
    const data = unwrap(
      await api.GET("/api/v1/admin/users", {
        params: { query: { limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) } },
      }),
    );
    rows.push(...data.users.map(toRow));
    cursor = data.next_cursor;
    if (cursor === undefined) {
      break;
    }
  }
  return rows;
}

export async function createUser(body: AdminCreateUserRequest): Promise<UserRow> {
  return toRow(unwrap(await api.POST("/api/v1/admin/users", { body })));
}

export async function updateUser(
  userId: string,
  body: components["schemas"]["UpdateUserAdminRequest"],
): Promise<UserRow> {
  return toRow(
    unwrap(await api.PATCH("/api/v1/admin/users/{userId}", { params: { path: { userId } }, body })),
  );
}

export async function forcePasswordReset(userId: string): Promise<TemporaryCredentials> {
  return unwrap(
    await api.POST("/api/v1/admin/users/{userId}/reset-password", {
      params: { path: { userId } },
    }),
  );
}

export async function listInvites(): Promise<Invite[]> {
  const invites: Invite[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page += 1) {
    const data = unwrap(
      await api.GET("/api/v1/admin/invites", {
        params: { query: { limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) } },
      }),
    );
    invites.push(...data.invites);
    cursor = data.next_cursor;
    if (cursor === undefined) {
      break;
    }
  }
  return invites;
}

export async function createInvite(body: CreateInviteRequest): Promise<CreatedInvite> {
  return unwrap(await api.POST("/api/v1/admin/invites", { body }));
}

/** Idempotent by contract: revoking one that is already gone answers 204. */
export async function revokeInvite(inviteId: string): Promise<void> {
  const { error, response } = await api.DELETE("/api/v1/admin/invites/{inviteId}", {
    params: { path: { inviteId } },
  });
  if (response.status !== 204) {
    throw new AdminError(response.status, error?.error.code ?? "unexpected");
  }
}

/**
 * Every live provisioning token. Unpaged by contract — `ScimTokenPage` carries
 * no cursor, because the list is the handful of credentials an instance's
 * identity provider holds, not a record of everything ever minted.
 */
export async function listScimTokens(): Promise<ScimToken[]> {
  return unwrap(await api.GET("/api/v1/admin/scim/tokens")).tokens;
}

/** The one response that carries the token itself; it is never shown again. */
export async function createScimToken(body: CreateScimTokenRequest): Promise<CreatedScimToken> {
  return unwrap(await api.POST("/api/v1/admin/scim/tokens", { body }));
}

export async function revokeScimToken(tokenId: string): Promise<void> {
  const { error, response } = await api.DELETE("/api/v1/admin/scim/tokens/{tokenId}", {
    params: { path: { tokenId } },
  });
  if (response.status !== 204) {
    throw new AdminError(response.status, error?.error.code ?? "unexpected");
  }
}

export async function getOrgSettings(): Promise<OrgSettings> {
  return unwrap(await api.GET("/api/v1/admin/org"));
}

export async function updateOrgSettings(body: UpdateOrgSettingsRequest): Promise<OrgSettings> {
  return unwrap(await api.PATCH("/api/v1/admin/org", { body }));
}

/**
 * The encryption mode's own endpoint, deliberately not a field on the settings
 * PATCH above: that screen saves as you type, and this one is a decision an
 * administrator should have to mean (openapi.yaml -> setOrgEncryptionMode).
 *
 * Throws `AdminError(409, "encryption_mode_locked")` when `compliance` is asked
 * for while it is not yet selectable — the screen keeps that option visible and
 * disabled, so the refusal should not be reachable from it, and is handled
 * anyway for the instance that locks it while the screen is open.
 */
export async function setOrgEncryptionMode(mode: EncryptionMode): Promise<OrgSettings> {
  return unwrap(
    await api.PUT("/api/v1/admin/org/encryption-mode", { body: { encryption_mode: mode } }),
  );
}

export interface AuditQuery {
  cursor?: string | undefined;
  action?: string | undefined;
  actorId?: string | undefined;
}

export async function listAuditEntries(query: AuditQuery): Promise<AuditPage> {
  return unwrap(
    await api.GET("/api/v1/admin/audit", {
      params: {
        query: {
          limit: 50,
          ...(query.cursor === undefined ? {} : { cursor: query.cursor }),
          ...(query.action === undefined ? {} : { action: query.action }),
          ...(query.actorId === undefined ? {} : { actor_id: query.actorId }),
        },
      },
    }),
  );
}
