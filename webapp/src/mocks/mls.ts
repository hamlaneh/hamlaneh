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
type MlsCommitPage = components["schemas"]["MlsCommitPage"];
type MlsCommit = components["schemas"]["MlsCommit"];
type SubmitMlsCommitRequest = components["schemas"]["SubmitMlsCommitRequest"];
type MlsWelcome = components["schemas"]["MlsWelcome"];
type MlsWelcomeList = components["schemas"]["MlsWelcomeList"];

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

interface MockState {
  devices: MockDevice[];
  groups: Map<string, MockGroup>;
  welcomes: MlsWelcome[];
  sequence: number;
}

function empty(): MockState {
  return { devices: [], groups: new Map(), welcomes: [], sequence: 0 };
}

let mls: MockState = empty();

/** Tests call this between cases. */
export function resetMockMls(): void {
  mls = empty();
}

/**
 * Registers a device for somebody who is not the signed-in user, so a test can
 * exercise the add-a-member path without a second browser.
 */
export function seedMockMlsDevice(userId: string, keyPackages: readonly string[]): MockDevice {
  const device: MockDevice = {
    id: uuid("de"),
    userId,
    signaturePublicKey: `fixture-signature-key-${userId}`,
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
        return errorResponse(404, "device_not_found", "No such device.");
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

  http.post<{ channelId: string }, ClaimMlsKeyPackagesRequest, MlsKeyPackageClaims | ApiError>(
    "/api/v1/channels/:channelId/mls/key-package-claims",
    async ({ params, request }) => {
      if (mockChannel(params.channelId) === undefined) {
        return channelNotFound();
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
      // Welcome was lost is a forked group.
      for (const delivery of body.welcomes ?? []) {
        mls.welcomes.push({
          id: uuid("we"),
          channel_id: params.channelId,
          group_id: group.groupId,
          device_id: delivery.device_id,
          welcome: delivery.welcome,
          created_at: new Date().toISOString(),
        });
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
      // Idempotent: deleting an already-acknowledged Welcome is still 204.
      mls.welcomes = mls.welcomes.filter((welcome) => welcome.id !== params.welcomeId);
      return new HttpResponse(null, { status: 204 });
    },
  ),
];
