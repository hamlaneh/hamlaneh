import { http, HttpResponse } from "msw";

import type { components } from "../api/schema";
import { CHAT_USERS, mockChannel } from "./chat";

type ApiError = components["schemas"]["Error"];
type MlsDevice = components["schemas"]["MlsDevice"];
type RegisterMlsDeviceRequest = components["schemas"]["RegisterMlsDeviceRequest"];
type ReplaceMlsKeyPackagesRequest = components["schemas"]["ReplaceMlsKeyPackagesRequest"];
type MlsKeyPackagePool = components["schemas"]["MlsKeyPackagePool"];
type MlsGroup = components["schemas"]["MlsGroup"];
type CreateMlsGroupRequest = components["schemas"]["CreateMlsGroupRequest"];
type ClaimMlsKeyPackagesRequest = components["schemas"]["ClaimMlsKeyPackagesRequest"];
type MlsKeyPackageClaims = components["schemas"]["MlsKeyPackageClaims"];
type MlsMemberDevicePage = components["schemas"]["MlsMemberDevicePage"];
type MlsCommitPage = components["schemas"]["MlsCommitPage"];
type MlsCommit = components["schemas"]["MlsCommit"];
type SubmitMlsCommitRequest = components["schemas"]["SubmitMlsCommitRequest"];
type MlsWelcome = components["schemas"]["MlsWelcome"];
type MlsWelcomeList = components["schemas"]["MlsWelcomeList"];
type MlsBackup = components["schemas"]["MlsBackup"];
type PutMlsBackupRequest = components["schemas"]["PutMlsBackupRequest"];

/**
 * The E2EE transport, mocked exactly as illiterately as the real server
 * (ADR 006, decision 2): every blob is stored and handed back unread, and the
 * only structured thing this file does with a commit is compare-and-swap its
 * epoch.
 *
 * That illiteracy is what makes the mock useful. A mock that understood MLS
 * could paper over a client bug; this one can only reproduce the sequencing
 * the client has to survive — first-wins per epoch, consuming claims, Welcomes
 * stored atomically with their commit.
 */

interface MockDevice {
  id: string;
  userId: string;
  signaturePublicKey: string;
  keyPackages: string[];
}

interface MockGroup {
  channelId: string;
  groupId: string;
  epoch: number;
  createdAt: string;
  commits: MlsCommit[];
}

/**
 * The one sealed envelope this account has stored (ADR 010). The mock is as
 * illiterate about it as the server: it holds the base64, mirrors the counter
 * the client claimed, and can compare that number and nothing else.
 */
interface MockBackup {
  envelope: string;
  counter: number;
  updatedAt: string;
}

interface MockState {
  devices: MockDevice[];
  groups: Map<string, MockGroup>;
  welcomes: MlsWelcome[];
  backup: MockBackup | null;
  sequence: number;
}

function empty(): MockState {
  return { devices: [], groups: new Map(), welcomes: [], backup: null, sequence: 0 };
}

let mls: MockState = empty();

/** Tests call this between cases. */
export function resetMockMls(): void {
  mls = empty();
}

/**
 * Registers a device for somebody who is not the signed-in user, so a test can
 * exercise the add-a-member path without a second browser.
 *
 * `signaturePublicKey` is what the member-devices directory will attribute to
 * this person, so a test that also puts a leaf in the tree for them has to
 * pass the key that leaf carries. The default is deliberately a key no leaf
 * ever holds: a seeded device that is never added to a group has nothing to
 * agree with, and one that is added and left on this default would be
 * evicted — visibly, rather than by accident.
 */
export function seedMockMlsDevice(
  userId: string,
  keyPackages: readonly string[],
  signaturePublicKey = `fixture-signature-key-${userId}`,
): MockDevice {
  const device: MockDevice = {
    id: uuid("de"),
    userId,
    signaturePublicKey,
    keyPackages: [...keyPackages],
  };
  mls.devices.push(device);
  return device;
}

/** What the mock is holding, for assertions. */
export function mockMlsGroup(channelId: string): MockGroup | undefined {
  return mls.groups.get(channelId);
}

export function mockMlsWelcomes(): MlsWelcome[] {
  return [...mls.welcomes];
}

export function mockMlsDevices(): MockDevice[] {
  return [...mls.devices];
}

/** The stored envelope, for assertions. */
export function mockMlsBackup(): MockBackup | null {
  return mls.backup === null ? null : { ...mls.backup };
}

/**
 * Plants an envelope the client did not upload — how a test plays the server
 * that lies. The counter is settable independently of what is sealed inside
 * the bytes, which is the entire point: it is what lets a test prove the
 * client checks the sealed one.
 */
export function seedMockMlsBackup(backup: MockBackup): void {
  mls.backup = { ...backup };
}

function uuid(tag: string): string {
  mls.sequence += 1;
  return `00000000-0000-4000-8000-${tag}${String(mls.sequence).padStart(10, "0")}`;
}

function errorResponse(status: number, code: string, message: string) {
  return HttpResponse.json<ApiError>({ error: { code, message } }, { status });
}

/** Every channel-scoped path answers 404 to a non-member, never 403. */
function channelNotFound() {
  return errorResponse(404, "channel_not_found", "No such channel.");
}

const ME = CHAT_USERS.me.id;

export const mlsHandlers = [
  http.post<never, RegisterMlsDeviceRequest, MlsDevice | ApiError>(
    "/api/v1/users/me/mls/device",
    async ({ request }) => {
      const body = await request.json();
      if (body.signature_public_key === "") {
        return errorResponse(400, "invalid_request", "A signature key is required.");
      }
      // Idempotent on (user, signature_public_key), so a client may call this
      // on every startup without bookkeeping.
      const existing = mls.devices.find(
        (device) =>
          device.userId === ME && device.signaturePublicKey === body.signature_public_key,
      );
      if (existing !== undefined) {
        return HttpResponse.json({
          id: existing.id,
          signature_public_key: existing.signaturePublicKey,
          created_at: "2026-08-29T09:00:00.000Z",
        });
      }
      const device: MockDevice = {
        id: uuid("de"),
        userId: ME,
        signaturePublicKey: body.signature_public_key,
        keyPackages: [],
      };
      mls.devices.push(device);
      return HttpResponse.json(
        {
          id: device.id,
          signature_public_key: device.signaturePublicKey,
          created_at: "2026-08-29T09:00:00.000Z",
        },
        { status: 201 },
      );
    },
  ),

  http.put<{ deviceId: string }, ReplaceMlsKeyPackagesRequest, MlsKeyPackagePool | ApiError>(
    "/api/v1/users/me/mls/devices/:deviceId/key-packages",
    async ({ params, request }) => {
      // A device may only publish to itself; anyone else's id is a 404.
      const device = mls.devices.find(
        (entry) => entry.id === params.deviceId && entry.userId === ME,
      );
      if (device === undefined) {
        return errorResponse(404, "mls_device_not_found", "No such device.");
      }
      const body = await request.json();
      // Replace-all: the previous unclaimed pool goes in the same breath.
      device.keyPackages = [...body.key_packages];
      return HttpResponse.json({ unclaimed_count: device.keyPackages.length });
    },
  ),

  http.get<{ channelId: string }, never, MlsGroup | ApiError>(
    "/api/v1/channels/:channelId/mls/group",
    ({ params }) => {
      if (mockChannel(params.channelId) === undefined) {
        return channelNotFound();
      }
      const group = mls.groups.get(params.channelId);
      if (group === undefined) {
        return errorResponse(404, "mls_group_not_found", "This channel has no group yet.");
      }
      return HttpResponse.json({
        group_id: group.groupId,
        epoch: group.epoch,
        created_at: group.createdAt,
      });
    },
  ),

  http.post<{ channelId: string }, CreateMlsGroupRequest, MlsGroup | ApiError>(
    "/api/v1/channels/:channelId/mls/group",
    async ({ params, request }) => {
      const channel = mockChannel(params.channelId);
      if (channel === undefined) {
        return channelNotFound();
      }
      if (!channel.e2ee) {
        return errorResponse(400, "e2ee_not_enabled", "This channel is not encrypted.");
      }
      if (mls.groups.has(params.channelId)) {
        return errorResponse(409, "mls_group_exists", "This channel already has a group.");
      }
      const body = await request.json();
      const group: MockGroup = {
        channelId: params.channelId,
        groupId: body.group_id,
        epoch: 0,
        createdAt: new Date().toISOString(),
        commits: [],
      };
      mls.groups.set(params.channelId, group);
      return HttpResponse.json(
        { group_id: group.groupId, epoch: 0, created_at: group.createdAt },
        { status: 201 },
      );
    },
  ),

  http.get<{ channelId: string }, never, MlsMemberDevicePage | ApiError>(
    "/api/v1/channels/:channelId/mls/member-devices",
    ({ params }) => {
      const channel = mockChannel(params.channelId);
      if (channel === undefined) {
        return channelNotFound();
      }
      if (!channel.e2ee) {
        return errorResponse(400, "e2ee_not_enabled", "This channel is not encrypted.");
      }
      // The fixture roster, exactly as the members endpoint answers it: every
      // user is in every channel. A test that needs a smaller roster — which
      // is all a removal looks like from here — overrides this handler.
      // One page: paging is the client's problem to get right, and the case
      // where it gets it wrong is written as an override too.
      return HttpResponse.json({
        members: Object.values(CHAT_USERS).map((user) => ({
          user_id: user.id,
          // Empty for somebody who has registered nothing, never omitted:
          // the contract's way to tell "has no device" from "not a member".
          signature_public_keys: mls.devices
            .filter((device) => device.userId === user.id)
            .map((device) => device.signaturePublicKey),
        })),
      });
    },
  ),

  http.post<{ channelId: string }, ClaimMlsKeyPackagesRequest, MlsKeyPackageClaims | ApiError>(
    "/api/v1/channels/:channelId/mls/key-package-claims",
    async ({ params, request }) => {
      const channel = mockChannel(params.channelId);
      if (channel === undefined) {
        return channelNotFound();
      }
      if (!channel.e2ee) {
        return errorResponse(400, "e2ee_not_enabled", "This channel is not encrypted.");
      }
      const body = await request.json();
      const devices = mls.devices.filter((device) => device.userId === body.user_id);
      const claims: MlsKeyPackageClaims["claims"] = [];
      const missing: string[] = [];
      for (const device of devices) {
        // Consuming: a key package handed out twice is the bug this endpoint
        // exists to make impossible.
        const keyPackage = device.keyPackages.shift();
        if (keyPackage === undefined) {
          missing.push(device.id);
          continue;
        }
        claims.push({ device_id: device.id, key_package: keyPackage });
      }
      return HttpResponse.json({ claims, missing_device_ids: missing });
    },
  ),

  http.get<{ channelId: string }, never, MlsCommitPage | ApiError>(
    "/api/v1/channels/:channelId/mls/commits",
    ({ params, request }) => {
      const group = mls.groups.get(params.channelId);
      if (mockChannel(params.channelId) === undefined || group === undefined) {
        return channelNotFound();
      }
      const url = new URL(request.url);
      const after = Number(url.searchParams.get("after_epoch") ?? "0");
      const limit = Number(url.searchParams.get("limit") ?? "50");
      return HttpResponse.json({
        commits: group.commits.filter((commit) => commit.epoch > after).slice(0, limit),
      });
    },
  ),

  http.post<{ channelId: string }, SubmitMlsCommitRequest, ApiError | null>(
    "/api/v1/channels/:channelId/mls/commits",
    async ({ params, request }) => {
      const group = mls.groups.get(params.channelId);
      if (mockChannel(params.channelId) === undefined || group === undefined) {
        return channelNotFound();
      }
      const body = await request.json();
      // The compare-and-swap that makes exactly one commit win each epoch.
      if (body.epoch !== group.epoch) {
        return errorResponse(
          409,
          "mls_epoch_conflict",
          "That epoch is no longer current.",
        );
      }
      group.epoch += 1;
      group.commits.push({
        epoch: group.epoch,
        message: body.message,
        created_at: new Date().toISOString(),
      });
      // Stored in the same breath as the commit: a committed add whose
      // Welcome was lost is a forked group. One delivery names many devices —
      // the blob is the same for all of them, the row is per device, which is
      // what lets each device fetch and acknowledge its own.
      for (const delivery of body.welcomes ?? []) {
        for (const deviceId of delivery.device_ids) {
          mls.welcomes.push({
            id: uuid("we"),
            channel_id: params.channelId,
            group_id: group.groupId,
            device_id: deviceId,
            welcome: delivery.welcome,
            created_at: new Date().toISOString(),
          });
        }
      }
      return new HttpResponse(null, { status: 201 });
    },
  ),

  http.get<never, never, MlsWelcomeList>("/api/v1/users/me/mls/welcomes", () => {
    const mine = new Set(
      mls.devices.filter((device) => device.userId === ME).map((device) => device.id),
    );
    return HttpResponse.json({
      welcomes: mls.welcomes.filter((welcome) => mine.has(welcome.device_id)),
    });
  }),

  http.delete<{ welcomeId: string }, never, ApiError | null>(
    "/api/v1/users/me/mls/welcomes/:welcomeId",
    ({ params }) => {
      // Always 204: an already-acknowledged, unknown or foreign id is a
      // silent no-op, so a guessed id confirms nothing.
      mls.welcomes = mls.welcomes.filter((welcome) => welcome.id !== params.welcomeId);
      return new HttpResponse(null, { status: 204 });
    },
  ),

  // The recovery surface (ADR 010).

  http.put<never, PutMlsBackupRequest, ApiError | null>(
    "/api/v1/users/me/mls/backup",
    async ({ request }) => {
      const body = await request.json();
      // The convenience rail, mirrored exactly: a counter that does not move
      // forward is refused, and that refusal is not the security control —
      // the client's floor against the SEALED counter is.
      if (mls.backup !== null && body.counter <= mls.backup.counter) {
        return errorResponse(409, "mls_backup_stale", "A newer backup is already stored.");
      }
      mls.backup = {
        envelope: body.envelope,
        counter: body.counter,
        updatedAt: new Date().toISOString(),
      };
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.get<never, never, MlsBackup | ApiError>("/api/v1/users/me/mls/backup", () => {
    if (mls.backup === null) {
      return errorResponse(404, "mls_backup_not_found", "This account has no stored backup.");
    }
    return HttpResponse.json({
      envelope: mls.backup.envelope,
      counter: mls.backup.counter,
      updated_at: mls.backup.updatedAt,
    });
  }),

  http.delete<never, never, null>("/api/v1/users/me/mls/backup", () => {
    // Idempotent: the asked-for state is already true when there is nothing.
    mls.backup = null;
    return new HttpResponse(null, { status: 204 });
  }),

  http.delete<{ deviceId: string }, never, ApiError | null>(
    "/api/v1/users/me/mls/devices/:deviceId",
    ({ params }) => {
      const device = mls.devices.find(
        (candidate) => candidate.id === params.deviceId && candidate.userId === ME,
      );
      if (device === undefined) {
        // Another account's device and an id naming nothing are one answer.
        return errorResponse(404, "mls_device_not_found", "No such device.");
      }
      mls.devices = mls.devices.filter((candidate) => candidate !== device);
      // The cascades, which are what keep a pending Welcome from becoming a
      // row nobody can ever acknowledge.
      mls.welcomes = mls.welcomes.filter((welcome) => welcome.device_id !== device.id);
      return new HttpResponse(null, { status: 204 });
    },
  ),
];
