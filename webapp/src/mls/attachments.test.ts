import { describe, expect, it } from "vitest";

import {
  ATTACHMENT_KEY_BYTES,
  ATTACHMENT_NONCE_BYTES,
  PAYLOAD_SENTINEL,
  decodeBody,
  encodeBody,
  newAttachmentKey,
  openAttachment,
  safeFilename,
  sealAttachment,
  sniffImageType,
} from "./attachments";
import { toBase64 } from "./bytes";

const bytes = (...values: number[]): Uint8Array => new Uint8Array(values);

const entry = (over: Record<string, unknown> = {}) => ({
  id: "11111111-1111-4111-8111-111111111111",
  key: toBase64(new Uint8Array(ATTACHMENT_KEY_BYTES)),
  name: "budget.pdf",
  type: "application/pdf",
  size: 1024,
  ...over,
});

describe("newAttachmentKey", () => {
  it("mints sixteen fresh bytes each time", () => {
    const first = newAttachmentKey();
    const second = newAttachmentKey();
    expect(first).toHaveLength(ATTACHMENT_KEY_BYTES);
    expect(toBase64(first)).not.toBe(toBase64(second));
  });
});

describe("sealAttachment / openAttachment", () => {
  it("round-trips a payload under its own variant", async () => {
    const key = newAttachmentKey();
    const plaintext = bytes(1, 2, 3, 4, 5);
    const sealed = await sealAttachment(key, "original", plaintext);

    expect(sealed).toHaveLength(ATTACHMENT_NONCE_BYTES + plaintext.length + 16);
    await expect(openAttachment(key, "original", sealed)).resolves.toEqual(plaintext);
  });

  it("gives every seal its own nonce", async () => {
    const key = newAttachmentKey();
    const first = await sealAttachment(key, "original", bytes(9));
    const second = await sealAttachment(key, "original", bytes(9));
    expect(toBase64(first)).not.toBe(toBase64(second));
  });

  it("refuses to open the original as the thumbnail", async () => {
    // The AAD is the domain separation ADR 013 fixes: the one substitution a
    // shared per-file key would otherwise permit is a server swapping an
    // attachment's two blobs, and this is what fails it.
    const key = newAttachmentKey();
    const sealed = await sealAttachment(key, "original", bytes(7, 7, 7));
    await expect(openAttachment(key, "thumb", sealed)).rejects.toThrow();
  });

  it("refuses a tampered byte", async () => {
    const key = newAttachmentKey();
    const sealed = await sealAttachment(key, "original", bytes(1, 2, 3));
    sealed.set([(sealed.at(-1) ?? 0) ^ 0xff], sealed.length - 1);
    await expect(openAttachment(key, "original", sealed)).rejects.toThrow();
  });

  it("refuses a blob too short to hold a nonce", async () => {
    const key = newAttachmentKey();
    await expect(openAttachment(key, "original", bytes(1, 2, 3))).rejects.toThrow();
  });

  it("refuses a key that is not sixteen bytes", async () => {
    await expect(sealAttachment(bytes(1, 2), "original", bytes(1))).rejects.toThrow();
  });
});

describe("encodeBody", () => {
  it("leaves a message with no files exactly as it was", () => {
    expect(encodeBody("hello", [])).toBe("hello");
  });

  it("wraps a message with files in the sentinel", () => {
    const encoded = encodeBody("here it is", [entry()]);
    expect(encoded.startsWith(PAYLOAD_SENTINEL)).toBe(true);
  });
});

describe("decodeBody", () => {
  it("reads an ordinary message as its own text", () => {
    expect(decodeBody("just words")).toEqual({ text: "just words", attachments: [] });
  });

  it("round-trips text and entries", () => {
    const original = entry({ width: 800, height: 600 });
    const decoded = decodeBody(encodeBody("look", [original]));
    expect(decoded).toEqual({ text: "look", attachments: [original] });
  });

  it("reports a payload that claims the sentinel but does not parse", () => {
    expect(decodeBody(`${PAYLOAD_SENTINEL}{not json`)).toBeNull();
    expect(decodeBody(`${PAYLOAD_SENTINEL}[]`)).toBeNull();
    expect(decodeBody(`${PAYLOAD_SENTINEL}{"text":42}`)).toBeNull();
  });

  it("drops an entry whose key is not an attachment key", () => {
    const decoded = decodeBody(
      `${PAYLOAD_SENTINEL}${JSON.stringify({
        text: "",
        attachments: [entry({ key: toBase64(bytes(1, 2, 3)) }), entry({ key: 12 })],
      })}`,
    );
    expect(decoded?.attachments).toEqual([]);
  });

  it("clamps a lying size and impossible dimensions", () => {
    const decoded = decodeBody(
      `${PAYLOAD_SENTINEL}${JSON.stringify({
        text: "",
        attachments: [entry({ size: -1, width: 1e12, height: Number.NaN })],
      })}`,
    );
    expect(decoded?.attachments[0]?.size).toBe(0);
    expect(decoded?.attachments[0]?.width).toBeUndefined();
    expect(decoded?.attachments[0]?.height).toBeUndefined();
  });

  it("keeps the text when the attachment list is missing", () => {
    const decoded = decodeBody(`${PAYLOAD_SENTINEL}${JSON.stringify({ text: "alone" })}`);
    expect(decoded).toEqual({ text: "alone", attachments: [] });
  });
});

describe("safeFilename", () => {
  it("keeps an ordinary name", () => {
    expect(safeFilename("budget.pdf")).toBe("budget.pdf");
  });

  it("keeps only the last path element", () => {
    expect(safeFilename("/home/amir/q3/secret.pdf")).toBe("secret.pdf");
    expect(safeFilename("C:\\Users\\amir\\secret.pdf")).toBe("secret.pdf");
  });

  it("strips the bidi overrides that disguise an extension", () => {
    // "exploit<RLO>gnp.exe" reads as "exploitexe.png" on the screen.
    expect(safeFilename("exploit\u202egnp.exe")).toBe("exploitgnp.exe");
  });

  it("strips control characters and collapses to one line", () => {
    expect(safeFilename("two\nlines\tin\u0000one")).toBe("two lines in one");
  });

  it("bounds the length", () => {
    expect(safeFilename("a".repeat(500))).toHaveLength(255);
  });

  it("reports an unusable name as empty", () => {
    expect(safeFilename("   ")).toBe("");
    expect(safeFilename("..")).toBe("");
  });
});

describe("sniffImageType", () => {
  it("recognises the four inline types", () => {
    expect(sniffImageType(bytes(0xff, 0xd8, 0xff, 0xe0))).toBe("image/jpeg");
    expect(sniffImageType(bytes(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a))).toBe("image/png");
    expect(sniffImageType(new TextEncoder().encode("GIF89a-------"))).toBe("image/gif");
    expect(sniffImageType(new TextEncoder().encode("RIFF????WEBPVP8 "))).toBe("image/webp");
  });

  it("refuses everything else, SVG and HTML included", () => {
    expect(sniffImageType(new TextEncoder().encode("<svg xmlns='http://www.w3.org/2000/svg'>"))).toBe(
      "",
    );
    expect(sniffImageType(new TextEncoder().encode("<!doctype html><script>"))).toBe("");
    expect(sniffImageType(bytes())).toBe("");
  });
});
