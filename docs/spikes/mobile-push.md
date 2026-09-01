# Spike — mobile push architecture with metadata minimisation

> **Status: evidence only. Nothing is decided here.** This file feeds a future ADR and closes
> nothing. PLAN.md §12's row "Mobile push architecture (metadata)" stays ⬜ Open until that ADR
> is written. Written by the orchestrator so the design pass reads a prepared record rather than
> repeating the search.
>
> **Checked on 2026-08-30.** Every version, limit and platform-behaviour claim below was verified
> that day against the source named in it. Push platforms change with OS releases and several
> claims here are version-dependent; §7 says which. Re-check before relying on any line.

ROADMAP Phase 3 ends with: *"Research spike: mobile push architecture with metadata minimization
(decision in PLAN.md §12)."* This is that spike. PLAN.md risk #11 is its motivation — *"users
expect native apps + push; push + E2EE leaks metadata and is genuinely hard"* — and PLAN.md §11
sets the near-term client: native mobile apps are post-v1, **PWA in the meantime**.

## 1. The framing that decides this, and it is not the one in the roadmap line

The roadmap line reads as *"how do we push without telling Apple and Google too much"*. That is
half the problem, and it turns out to be the easier half. **Hamlaneh has two untrusted parties in
a push, and the second one is ours.**

PLAN.md §6.1 names adversary 3 as *"a compromised or untrusted server — including hosting
providers"*, and [ADR 006](../adr/006-mls-library-and-boundaries.md) spent a phase making the
server MLS-blind so that adversary gets ciphertext. A push notification whose text the server
composes hands that adversary back a channel into the user's lock screen — one the user reads as
if it came from a person, and one no MLS key signs. The server does not need to break the
crypto to write *"Legal: approve the wire transfer"* under a colleague's name.

So the design space has exactly one axis, and it is **where the notification's human-readable
text is produced**:

| | Where the text comes from | Provider sees the text? | Server can forge the text? |
|---|---|---|---|
| **A** | The server composes it | native: **yes** · web push: no (§3) | **yes** |
| **B** | The device decrypts and composes it | no | **no** — a forgery degrades to "New message" |
| **C** | There is no push | n/a | n/a — and no notification either |

Everything below is evidence for choosing among those three. §2–§5 are the findings; §6 puts them
against Hamlaneh specifically; §7 says what is still unknown; §8 asks the three questions.

### A project-specific wrinkle nobody has written down: the server cannot compute mentions

`server/internal/storage/messages.go` derives mentions server-side — `parseMentions(nm.Content)`
— and only members of the channel get a row. Under **Strict E2EE the server has no content**, so
it cannot compute them. The push trigger therefore degrades from *"you were mentioned"* to
*"something happened in a channel you belong to"*, unless the client states its mention list in
the envelope — which hands the server the social-graph edge that mention parsing was reading out
of plaintext anyway. This is not a push problem that arrives with push; it is an E2EE problem
that push is the first feature to make visible.

Matrix hit the same wall and wrote it down: *"When encrypted events are present, the homeserver
is unable to conclusively run push rules — in this case, the client will need to run them
locally"*
([sygnal](https://github.com/element-hq/sygnal/blob/main/docs/applications.md)). That is
independent confirmation that the constraint is structural rather than ours, and it is *why*
encrypted Matrix rooms are stuck with the minimal push format regardless of anyone's preference.
Named here so the ADR budgets for it.

## 2. What APNs and FCM actually require and see (the native path)

### Apple, APNs

| | |
|---|---|
| Max payload | **4 KB (4096 bytes)**; VoIP **5 KB (5120 bytes)** ([Apple](https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns)) |
| Encrypted payload supported? | **Not by the platform.** APNs carries whatever JSON you give it, in the clear to Apple. Confidentiality is the app's job, done in a Notification Service Extension (below) |
| Background (silent) push | `apns-push-type: background`, `apns-priority: 5`, `aps` containing only `content-available` and **no** alert/sound/badge key ([Apple](https://developer.apple.com/documentation/usernotifications/pushing-background-updates-to-your-app)) |
| Background push reliability | Apple's own words: *"the system doesn't guarantee their delivery"*; *"The system may throttle the delivery of background notifications if the total number becomes excessive… don't try to send more than two or three per hour."* Force-quit the app and *"the system discards the held notification"* |

**The Notification Service Extension is the whole native answer, and Apple documents it as such.**
An NSE runs on-device before the notification is shown and Apple's own example payload is a
decryption case — `"body": "(Encrypted)"` alongside an `ENCRYPTED_DATA` key
([Apple](https://developer.apple.com/documentation/usernotifications/modifying-content-in-newly-delivered-notifications)).
Two constraints follow from that same page and they are the ones that bite:

- The extension runs **only** when the payload carries `mutable-content: 1` **and** an `alert`
  dictionary with at least a title, subtitle or body. **A truly empty push cannot run an NSE.**
  Every E2EE app on iOS therefore ships a placeholder string that Apple sees — Apple's own
  example ships `"(Encrypted)"`.
- ~30 seconds, then `serviceExtensionTimeWillExpire()`; fail to call the handler from either
  method and *"the system displays the original notification contents"* — i.e. the placeholder.

### Google, FCM

| | |
|---|---|
| Max payload | **4096 bytes** for both notification and data messages ([Firebase](https://firebase.google.com/docs/cloud-messaging/concept-options)) |
| Encrypted payload supported? | Same answer: not by the platform. A *data message* is handed to the app rather than rendered by the SDK, which is what makes on-device decryption possible |
| Offline storage | FCM stores messages per TTL, with a documented **limit of 100 pending messages** per device before they are dropped ([Firebase](https://firebase.google.com/docs/cloud-messaging/understand-delivery)) |
| What Google records | Delivery metrics broken down **by application, date and analytics label**, plus a BigQuery export carrying **individual message logs** — acceptance timestamps, delivery status, platform, priority, TTL, collapse keys ([Firebase](https://firebase.google.com/docs/cloud-messaging/understand-delivery)) |
| Client requirement | *"devices running Android 6.0 or higher that also have the Google Play Store app installed"* ([Firebase](https://firebase.google.com/docs/cloud-messaging/android/client)) — so FCM does not exist on de-Googled devices |

### What the provider learns from a push carrying nothing at all

This is the number that matters and it is not zero. Even a content-free push tells Apple or
Google: the **device token** (which they can map to the device and, at Apple, to an Apple
account), the **app identity** (bundle ID), the **timestamp**, and by accumulation the
**frequency and size distribution** — that is, when you are talked to and how much.

That this is a live intelligence source rather than a theoretical one is established, and the
primary sources are unusually good. Senator Wyden's letter to the Attorney General of
**6 December 2023**: *"The data these two companies receive includes metadata, detailing which
app received a notification and when, as well as the phone and associated Apple or Google account
to which that notification was intended to be delivered."* And on the absence of an exit:
*"app developers don't have many options; if they want their apps to reliably deliver push
notifications on these platforms, they must use the service provided by Apple or Google"*
([Wyden letter](https://www.wyden.senate.gov/imo/media/doc/wyden_smartphone_push_notification_surveillance_letter.pdf)).

Apple has since formalised the practice in two documents. Its **Legal Process Guidelines**
(US, published October 2025) carry a dedicated APNs section: *"The Apple ID associated with a
registered APNs token and associated records may be obtained with an order under 18 U.S.C.
§2703(d) or a search warrant"*
([Apple](https://www.apple.com/legal/privacy/law-enforcement-guidelines-us.pdf)). And its
transparency report now has a **Push Token Requests** category, which Apple says it was *"only
permitted to disclose… starting in Transparency Report Period July 1–December 31, 2022"*, such
requests *"generally seek identifying details of the Apple Account associated with the device's
push token"* ([Apple](https://www.apple.com/legal/transparency/push-token.html)).

**Encrypting the payload does not touch any of this.** It is the irreducible cost of using a
platform push service, and the honest framing for §6 is that the choice is *how much beyond this*
to concede.

**One asymmetry is worth carrying into the design.** Apple's two-or-three-per-hour throttle and
its delivery disclaimer apply to `content-available` **background** pushes. A `mutable-content`
**alert** push — the kind that runs an NSE — is a different push type with different treatment.
Signal's server splits along exactly that line: urgent new-message pushes go out as alert type
with `mutable-content`, non-urgent ones as background
([APNSender.java](https://github.com/signalapp/Signal-Server/blob/main/service/src/main/java/org/whispersystems/textsecuregcm/push/APNSender.java)).
Apple does not publish a budget for the alert path; §7 records that as unestablished rather than
as absence of a limit.

### Calls are the hardest native case, and the PWA cannot do it at all

Phase 2 shipped calls, so ringing a phone matters. On iOS, waking a terminated app to ring
requires PushKit, and since iOS 13 a PushKit push **must** report an incoming call to CallKit in
the same run loop or the system kills the app and eventually stops delivering VoIP pushes to it
([Apple Developer Forums, Apple staff](https://developer.apple.com/forums/thread/126853)). On
Android, full-screen intents — the ringing UI — became a restricted permission in Android 14,
granted by default only to apps whose core function is calling or alarms, enforced through a Play
Console declaration
([Android](https://developer.android.com/about/versions/14/behavior-changes-14) ·
[Play Console Help](https://support.google.com/googleplay/android-developer/answer/13392821)).
Both are native-app-only capabilities. **No PWA rings a phone**, and no amount of push design
changes that.

## 3. What a PWA can do today — and the finding that reframes the whole spike

PLAN.md §11 makes the PWA the interim mobile story. It turns out to be the *better* story on the
metadata axis, for one structural reason.

**Web Push encrypts the payload end-to-end between the application server and the browser, by
specification.** [RFC 8291](https://www.rfc-editor.org/rfc/rfc8291.html) defines an `aes128gcm`
content encoding whose keys are held by the subscribing user agent and the application server —
nobody else. [RFC 8030](https://www.rfc-editor.org/rfc/rfc8030.html) is explicit about why:
*"The protection afforded by TLS does not protect content from the push service. Without
additional safeguards, a push service can inspect and modify the message content"*, and therefore
*"Applications using this protocol MUST use mechanisms that provide end-to-end confidentiality,
integrity, and data origin authentication."*

So on the web path, **Apple and Google's push services relay ciphertext whatever the payload
says.** The classic trade — "put the sender's name in the push and Google reads it" — does not
exist here. What remains is §1's other adversary: the server still *wrote* that ciphertext.

Concrete numbers and conditions:

| | |
|---|---|
| Payload budget | 4096 octets of body → **at most 3993 octets of plaintext** ([RFC 8291 §4](https://www.rfc-editor.org/rfc/rfc8291.html)). Payloads are arbitrary octets, not JSON — the `push` event exposes `data.arrayBuffer()`, so there is no base64 tax |
| Vendor account needed? | **None.** Apple: *"You don't need to join the Apple Developer Program to send web push notifications"* ([Apple](https://developer.apple.com/documentation/usernotifications/sending-web-push-notifications-in-web-apps-and-browsers)). Chrome: *"You no longer need a Firebase project, a `gcm_sender_id`, or an `Authorization` header"* ([Chrome](https://developer.chrome.com/blog/web-push-interop-wins), 2016 — old, but still the architecture) |
| Credentials | A self-generated **VAPID** P-256 keypair ([RFC 8292](https://www.rfc-editor.org/rfc/rfc8292.html)). Apple validates the JWT and rejects `BadJwtToken` if *"The JWT subject claim isn't a URL or `mailto:`"* — so the `sub` contact is effectively **required** by Apple, and reaches Apple with every push |
| iOS support | **iOS/iPadOS 16.4+**, and only for **Home Screen web apps** whose manifest sets `display` to `standalone` or `fullscreen`, with the permission request in *"response to direct user interaction"* ([WebKit](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/)). Push API is Baseline "widely available" since March 2023 ([MDN](https://developer.mozilla.org/en-US/docs/Web/API/Push_API)) |
| Silent push | **Forbidden on Safari.** Apple: *"Safari doesn't support invisible push notifications. Present push notifications to the user immediately after your service worker receives them. If you don't, Safari revokes the push notification permission for your site."* WebKit's framing: *"The Web Push API is not an invitation for silent background runtime"* ([WebKit](https://webkit.org/blog/12945/meet-web-push/)). Chrome and Edge reject a subscription unless `userVisibleOnly: true` ([MDN](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)) and otherwise show *"This site has been updated in the background"*. Firefox applies a quota that messages generating notifications are exempt from |
| Egress | The instance's server connects outbound to `https://*.push.apple.com`, `https://fcm.googleapis.com/fcm/send/…`, `https://updates.push.services.mozilla.com` |

**Two widely repeated claims are wrong and worth killing here**, because both would have changed
the conclusion:

- *"iOS web push is unavailable in the EU."* Apple announced removing Home Screen web apps under
  the DMA in February 2024 and **reversed it on 1 March 2024**, before iOS 17.4 shipped
  ([TechCrunch](https://techcrunch.com/2024/03/01/apple-reverses-decision-about-blocking-web-apps-on-iphones-in-the-eu/) ·
  [9to5Mac](https://9to5mac.com/2024/03/01/apple-home-screen-web-apps-ios-17-eu/)).
- *"iOS wipes PWA storage after 7 days."* The seven-day cap on script-writable storage —
  IndexedDB, Cache API, service worker registrations, localStorage — is a **Safari** policy.
  WebKit, primary: *"Web applications added to the home screen are not part of Safari and thus
  have their own counter of days of use… We do not expect the first-party in such a web
  application to have its website data deleted"*
  ([WebKit](https://webkit.org/blog/10218/full-third-party-cookie-blocking-and-more/)). This
  matters directly: the MLS keystore lives in IndexedDB.

### What is missing in our tree is exactly one file, and the stated reason not to have it does not apply

`webapp/public/brand/manifest.json` already sets `"display": "standalone"` — the iOS
precondition is met today. The only missing piece is a service worker, and ROADMAP §1.5 gives a
specific reason for not having one:

> *no service worker, deliberately: cookie auth plus a realtime socket makes a cached shell a
> correctness hazard, not an offline story*

That reason is about **caching** — a `fetch` handler serving a revoked session a stale shell. A
push-only service worker registers no `fetch` handler and caches nothing. The recorded objection
does not reach it. (`webapp/public/mockServiceWorker.js` is MSW's dev-only artefact and is not a
production service worker.)

## 4. How comparable products handle it

**The pattern is unanimous, and it is option B**: the push is a wake-up, the device fetches and
decrypts, the device writes the text. Nobody in this list composes the notification server-side.
What differs is how much the wake-up itself concedes, and who holds the credentials.

Here is the whole comparison in one table, then what each row costs. Payloads are quoted from the
products' own server source where it exists.

| | What goes to APNs / FCM | iOS NSE | Conceded to Apple/Google |
|---|---|---|---|
| **Signal** | **Nothing.** APNs: `{"aps":{"mutable-content":1,"alert":{"loc-key":"APN_Message"}}}`. FCM: `{"newMessageAlert":""}` | Yes — fetches over Signal's own connection, decrypts, 30 s budget | token, app id, timing, frequency |
| **WhatsApp** | Not documented anywhere by WhatsApp. Measured as wake-up-only with no leakage | Almost certainly, but **no primary source** | token, app id, timing, frequency (measured) |
| **Matrix / Element** | Default: `room_id`, `event_id`, **sender display name**, and for unencrypted rooms **message content**. `event_id_only`: `room_id`, `event_id`, counts | Yes, gated by a user setting | the above, **plus** everything to the gateway operator |
| **Wire** | `{"aps":{"alert":{"title":"New message"},"mutable-content":"1"},"data":{"type":"notice","data":{"id":<nid>},"user":<uuid>}}` — **the recipient's user UUID in cleartext** | Inferred from server source, not verified in client source | the above, **plus a stable per-user identifier** |
| **Zulip** | The real notification content, sealed under a per-device key | n/a (relay design) | token, timing, ciphertext — via Zulip's own bouncer |

**Signal** ships the strictest version and its payload is two hard-coded constants with no
per-message data in them at all — built at class-load time in
[`APNSender.java`](https://github.com/signalapp/Signal-Server/blob/main/service/src/main/java/org/whispersystems/textsecuregcm/push/APNSender.java),
with the FCM side a single data key carrying an empty value in
[`FcmSender.java`](https://github.com/signalapp/Signal-Server/blob/main/service/src/main/java/org/whispersystems/textsecuregcm/push/FcmSender.java).
`SignalNSE/NotificationService.swift` then connects to Signal's own service, fetches, and
decrypts locally
([Signal-iOS](https://github.com/signalapp/Signal-iOS/blob/main/SignalNSE/NotificationService.swift)).
**The failure path is the instructive part**: Signal's own header comment records that *"If the
extension takes too long to perform its work (more than 30s), it will be notified and immediately
terminated"*, and on expiry it completes with an **empty** notification content rather than
letting the placeholder through — *"By default the OS will present whatever the raw content of the
original notification is to the user otherwise."* When decryption cannot happen, Signal shows
nothing. That is option B's failure mode, chosen deliberately and shipped.

On Android, Signal's FCM handler reads only three control keys and ignores the message body
entirely, falling through to a fetch
([FcmReceiveService.java](https://github.com/signalapp/Signal-Android/blob/main/app/src/main/java/org/thoughtcrime/securesms/gcm/FcmReceiveService.java)),
and a websocket-only mode exists for devices without Play Services — visible in source, not
documented publicly.

**Wire is the closest architectural comparison — same MLS, same OpenMLS lineage as
[ADR 006](../adr/006-mls-library-and-boundaries.md) — and it is the cautionary one.** Its rule is
right: *"a native push is only to wake up the client so it can pull the actual notification
payload from the queue (`GET /notifications`)"*
([wire-server#549](https://github.com/wireapp/wire-server/pull/549)). But its serialiser puts the
recipient's **Wire user UUID in cleartext** in the payload, with the comment *"Since we have no
useful data here, we send a default payload that gets overridden by the client"*
([Gundeck/Push/Native/Serialise.hs](https://github.com/wireapp/wire-server/blob/develop/services/gundeck/src/Gundeck/Push/Native/Serialise.hs)).
Wire's January 2025 Privacy Whitepaper describes this accurately. Its **July 2021 Security
Whitepaper did not**: §4.5 said of the push providers *"The content is encrypted and not visible
to the external push providers"* — true of message content, false of the UUID sitting beside it
in the same JSON. The May 2025 Security Whitepaper drops the sentence.

That is the most useful thing in this section for a project whose principle #4 is *honesty over
hype*: **the closest product to ours shipped a whitepaper claim its own source code contradicted,
and quietly removed it four years later.** A stable per-user identifier handed to Apple and
Google on every message is precisely the metadata a wake-up-only design exists to avoid, and it
got there by nobody re-reading the serialiser against the whitepaper. Independent measurement
caught it: the peer-reviewed PETS 2024 study of 21 messengers classified Wire as leaking user
identifiers to the push service — one of only three wake-up-only apps in the study that leaked
anything ([Samarin et al., PETS 2024](https://arxiv.org/abs/2407.10589)).

**Matrix / Element** is the best-documented because the trade is written into the spec. A
homeserver pushes to a *push gateway* (typically [sygnal](https://github.com/matrix-org/sygnal)),
which holds the APNs and FCM credentials and forwards; Element's iOS app decrypts in an NSE. The
privacy-minimising format is `event_id_only`, and the
[Push Gateway API spec](https://spec.matrix.org/latest/push-gateway-api/) states that under it
*"only the `event_id`, `room_id`, `counts`, and `devices` are required to be populated"* —
stripping sender, content and event type. **Note what survives even so: the `room_id` and the
unread `counts`.** The most metadata-conscious option in the most metadata-conscious product on
this list still tells the gateway which room and how many.

Two details matter more than the format name. First, **the default is not the safe one**: sygnal's
documented default APNs payload carries `room_id`, `event_id` and the sender's display name in
`loc-args`, and its own guidance is a warning rather than a default — *"**Consider user privacy**:
if you use the `event_id_only` format, then data sent to the notification service (FCM or APNs) is
minimal. If you do not, then unencrypted messages will have their content sent to the notification
service, which some may prefer to avoid"*
([sygnal](https://github.com/element-hq/sygnal/blob/main/docs/applications.md)). Second, **the
spec says nothing about any of this** — the Push Gateway API contains no privacy section at all,
so the only guidance a Matrix implementer gets lives in one gateway's docs.

**Zulip** is the most directly instructive, because it is self-hosted, it faced this exact
question, and it answered it recently. Its documentation is blunt about the floor: *"Google's and
Apple's security model for mobile push notifications does not allow self-hosted Zulip servers to
directly send mobile notifications to the Zulip mobile apps"*, so Zulip operates a **central
forwarding service ("bouncer")** that every self-hoster's server pushes through. From Zulip
Server 12.0 that hop is end-to-end encrypted: the payload is sealed with libsodium's
`crypto_secretbox_easy` (XSalsa20-Poly1305) under a per-device symmetric key, so *"The
notification contents — including the message, sender, recipient, channel, and topic — are inside
an encrypted ciphertext that Apple, Google, and the service cannot decrypt"*
([Zulip](https://zulip.readthedocs.io/en/stable/production/mobile-push-notifications.html)).
This is the **content-blind relay** that §5 concludes native is stuck with, built and shipping —
and note it is the one product here that puts real content in the payload rather than making the
device fetch, which it can only do *because* it encrypts it first.

**Mattermost** documents the same mitigation one notch cruder: send **only a message ID**, so
that *"Apple and Google handle only the ID and are unable to read any part of the message
itself"* ([Mattermost](https://docs.mattermost.com/deployment-guide/mobile/mobile-faq.html)).
Its credential story is §5's.

**WhatsApp publishes nothing about push, and that is itself the finding.** Its Encryption
Overview whitepaper (v9, 25 February 2026) was downloaded and searched: **zero** occurrences of
"push", "notif", "APNs", "Apple", "Google" or "Firebase", against 222 of "encrypt" — so the
extraction worked and the silence is real
([whitepaper](https://www.whatsapp.com/security/WhatsApp-Security-Whitepaper.pdf)). Its privacy
policy does not name Apple or Google as push providers either. Behaviourally it must use an NSE —
it renders sender and preview while claiming E2EE — but **no primary source confirms the
mechanism**, and PETS 2024's measurement is the only independent evidence: WhatsApp was
classified wake-up-only with no observed leakage to FCM.

**The measurement study is worth its own line**, because it is the only place anyone has checked
these claims rather than repeating them. PETS 2024 tested 21 messengers: 20 used FCM, **11 leaked
metadata and 4 leaked message contents**, and *"none of the data we observed being leaked to FCM
was specifically disclosed in those apps' privacy disclosures"*
([Samarin et al.](https://arxiv.org/abs/2407.10589)). Its definition of the good case is the
target to design against: *"the only metadata that FCM receives is that the user received some
message or messages, and when that push notification was issued."*

## 5. UnifiedPush and the self-hosted alternatives

**The headline finding: UnifiedPush 3.x *is* Web Push.** Its Android specification
(`AND_3.1.0`) normatively requires the endpoint to be *"the URL of the push resource as defined
by RFC8030"*, the message to be *"an encrypted content that follows RFC8291"*, and a **VAPID**
public key so the application server can authenticate per RFC 8292
([UnifiedPush](https://unifiedpush.org/developers/spec/android/)). A single standard Web Push
implementation in the Go server serves the PWA on desktop, the PWA on Android, the PWA on iOS
Home Screen, **and** UnifiedPush on Android — with no Apple Developer Program membership and no
Firebase project. UnifiedPush support is not a feature to build; it is a consequence of building
the standard thing.

What it costs in reach, from its own documentation:

- **iOS: no.** UnifiedPush's FAQ: *"iOS doesn't support running services in the background, so
  running a UnifiedPush distributor won't be possible without jailbreaking or Apple's approval
  for the foreseeable future"* ([FAQ](https://unifiedpush.org/users/faq/)). The site advertises
  Android and Linux only. (Beware the name collision: Red Hat's archived AeroGear "UnifiedPush
  Server" is a different, unrelated project that *does* speak APNs.)
- **Android: a separate distributor app** (ntfy, NextPush, Sunup) unless the app embeds the
  official [`embedded_fcm_distributor`](https://github.com/UnifiedPush/android-embedded_fcm_distributor)
  fallback — which is FCM again. This is what Element's Play build does: *"For Gplay variant it
  means that FCM will be used by default, but user can choose another UnifiedPush distributor"*
  ([element-android](https://github.com/element-hq/element-android/blob/develop/docs/unifiedpush.md)).
  Play distribution is not a blocker: Element, Tusky, Fedilab and ntfy all ship on Play with it.
- **Battery:** UnifiedPush's FAQ claims it *"should not increase the application's battery
  consumption"*. The cost has moved into the distributor rather than vanished: ntfy implements
  instant delivery with **a foreground service and a persistent notification**, and without it
  *"messages may arrive with a significant delay (sometimes many minutes, or even hours later)"*
  ([ntfy](https://docs.ntfy.sh/subscribe/phone/)). That is Doze doing what Android documents it
  does — *"Suspends network access. Ignores wake locks… Doesn't let JobScheduler run"* — with
  high-priority FCM named as the exemption
  ([Android](https://developer.android.com/training/monitoring-device-state/doze-standby)).

**ntfy self-hosted** is the sharpest illustration of the platform floor, in its own words:
*"Unlike Android, iOS heavily restricts background processing, which sadly makes it impossible to
implement instant push notifications without a central server."* Self-hosted ntfy must set
`upstream-base-url: "https://ntfy.sh"` and forward poll requests to an APNs-connected upstream to
get instant iOS delivery, sending a message ID and a **SHA256 of the topic URL**, with the device
then fetching real content from your server ([ntfy](https://docs.ntfy.sh/config/)). Self-hosting
ntfy buys a Google-free Android path; it does not buy iOS independence.

**The credential problem, which is what actually decides the native path.** APNs and FCM
credentials are bound to the app's signing identity and bundle ID, so whoever pushes must be
whoever shipped the binary. Every precedent lands on the same two shapes. Mattermost is explicit:
its hosted proxy (HPNS) is *"an option for those using App Store/Play Store versions of the
app"*, and otherwise *"if you compile the apps yourself, you must also compile and use your own
MPNS with the corresponding secret"*
([Mattermost](https://docs.mattermost.com/deployment-guide/mobile/host-your-own-push-proxy-service.html)).
Rocket.Chat says the same — a custom gateway means white-labelling the mobile app
([Rocket.Chat](https://docs.rocket.chat/docs/push-notifications)).

**Element spells it out without hedging**: you may run your own sygnal, but then *"you can not
use the stock Element Apps, but will need to upload your own version of the Element App"*, and
*"You will need to configure your Sygnal with the private key of your Element App"*
([Element docs](https://docs.element.io/latest/element-server-suite-classic/advanced-configuration/notifications-mdm-push-gateway/)).
**Wire puts a commercial gate on the same door**: its install documentation's section on enabling
push with the public App Store / Play Store clients reads *"You need to get in touch with us.
Please talk to sales or customer support… If a contract agreement has been reached, we can set up
a separate AWS account for you"*; without that, a self-hoster runs a mock SNS and gets
**websocket-only** delivery — no notification when the app is not connected
([wire-server](https://github.com/wireapp/wire-server/blob/develop/docs/src/how-to/install/infrastructure-configuration.md)).
Wire's own escape hatch for users who object is an **F-Droid build preconfigured to skip FCM and
use websockets**, which its whitepaper concedes *"may lead to increased battery consumption"*.

That is the shape of the trap, stated by the two products closest to ours: an AGPL server anyone
can run, and a push path that requires either the vendor's blessing or your own app in the store.

**So for native apps there is no metadata-free option that also has reach. There is only a choice
of who holds the metadata:** a project-run relay (reach; Hamlaneh holds every instance's push
metadata) or per-operator app builds (ownership; effectively zero reach for a homelab user, who
would need an Apple Developer account and their own App Store submission). Zulip's bouncer (§4)
is the existence proof that the relay can at least be made **content-blind** — it forwards a
ciphertext it cannot open — so the concession is timing, volume and device tokens rather than
messages. PLAN.md §11 puts native post-v1, so this is a real decision that does not have to be
made now, but it should be recorded as unavoidable rather than rediscovered later as a surprise.

## 6. The realistic options for Hamlaneh

Reading the columns the task asked for. "Provider" is Apple/Google/Mozilla; "server" is a
hostile Hamlaneh instance under PLAN §6.1 adversary 3.

| | What the push provider learns | What the user gets | What breaks if the server is hostile | Reach |
|---|---|---|---|---|
| **A — Web Push, server writes the text** | Endpoint↔device, timing, frequency, ciphertext size, our VAPID `sub` and server IP. **Not the text** (RFC 8291) | A named sender and channel. No message preview — in Strict mode the server does not have one | **It can forge any notification** and there is no way for the user to tell. It also proves it holds the metadata it just displayed | PWA on iOS 16.4+ Home Screen, all Android browsers, desktop. No ringing |
| **B — Web Push, service worker decrypts and writes the text** | Identical to A — the payload is opaque either way | Real sender and preview, produced by the same MLS keys as the message | A forged push **degrades to "New message"**, because a payload the SW cannot decrypt has no text to show. The server keeps two powers it always had: withholding pushes, and spending the user's battery on decoys — and on Safari a decoy is always *visible*, since silent push is forbidden | Same as A, **if** the SW can run the MLS core (§7, §8 Q2) |
| **B′ — as B, ciphertext carried in the push** | Same, plus a slightly larger and more informative size distribution | Same as B, and it works with no network at notification time | Same as B | Same as B, for messages under the budget. §3's 3993-octet plaintext budget against the project's own measured ~145-byte MLS framing overhead ([mls-wasm-integration §4](mls-wasm-integration.md)) leaves roughly **3.8 KB of message plaintext** — most chat messages, not all |
| **C — no push (status quo)** | Nothing | Nothing when the app is closed. This is PLAN risk #11 as a lived experience | Nothing new | n/a |
| **D — native apps + project-run relay (post-v1)** | Everything in A/B **plus the bundle ID** — Apple and Google learn the user runs Hamlaneh specifically, and every instance's users pool under one app identity | Everything, including **ringing** (CallKit / full-screen intents), which no PWA can do | Same as A or B depending on where the text is produced — **plus** the relay operator (us) sees every instance's push volume and timing | Full |
| **E — UnifiedPush** | Whatever the chosen push server learns; can be the user's own | Same as B | Same as B | **Android only**, distributor app required. Free to support because it is the same code as A/B |

Four things deserve a sentence they do not fit in the table.

**Option B′ is Zulip's design with one difference that is the whole point.** Zulip also puts
ciphertext in the push, but the key is one its own server generated, so the scheme protects the
message from the bouncer and from Apple and Google — not from Zulip's server, which is not
E2EE and never claimed to be. B′ carries the **MLS** ciphertext, which the Hamlaneh server could
not read in the first place. Same shape, strictly stronger property, and it costs nothing extra
because the ciphertext already exists.

**Option B's residual weakness is honest and small.** Safari revokes push permission for a site
that receives a push and shows nothing, so the service worker must always display *something* —
which means a hostile server can always make a phone buzz. What it cannot do is make the buzz
*say* anything. That is a much weaker attack than A allows, and it is the strongest property
available on any platform.

**Option D is not a competitor to A/B; it is a later addition.** Native apps do not remove the
web path, and a native app can carry the same content-free-wake-up design. The genuinely new
decision native forces is the relay, not the payload.

**Self-hosting fragments the push population, and that is an unplanned privacy win.** A browser
subscribes per *origin*, so each instance is a separate subscribing site — `chat.acme.example`
rather than a single product-wide bundle ID shared by every Hamlaneh user alive. A native app
pools them. How much of this survives contact with reality depends on what Apple and Google
actually record about the subscribing origin, which neither documents (§7).

## 7. What this spike does not establish

Named, rather than left for someone to assume the opposite.

- **Nothing was built and nothing was measured.** No service worker was written, no push was
  sent, no device was touched. Every "can the service worker do X" below is a reading of specs,
  not an observation.
- **Whether the MLS WASM core runs in a service worker at all is untested**, and it is the
  load-bearing unknown for options B/B′. The core is ~489 KB gzipped
  ([mls-wasm-integration §2](mls-wasm-integration.md)); a push handler's time and memory budget
  on iOS is undocumented. Instantiating it per push may simply be too slow. **Only a real iPhone
  and a real Android device can answer this** — no amount of further reading will.
- **Whether `keystore.ts` opens from a service worker is untested.** It looks favourable: the
  wrapping key is a non-extractable `CryptoKey` in IndexedDB with no passphrase, so a same-origin
  service worker should be able to use it without user interaction, and `withKeystoreLock` already
  uses `navigator.locks`, which is origin-scoped across contexts and Baseline since March 2022.
  "Should" is doing real work in that sentence.
- **The MLS ratchet-state hazard is identified but not solved.** Decrypting advances state; two
  contexts decrypting the same message is a correctness bug, not a UX one. §8 Q3 asks it.
- **Battery and wake-frequency costs are unquantified.** A busy channel is a lot of pushes, and
  on Safari every one of them is visible by rule.
- **iOS's Home-Screen-only condition was not re-confirmed against a 2026 primary source.** Apple
  and WebKit still document the 16.4 scope and nothing was found lifting it through iOS 18/26,
  but the confirming search was of secondary sources.
- **No push provider's actual retention or disclosure practice was established beyond the Wyden
  record.** What Apple and Google *keep*, and for how long, is not documented by them.
- **What the push service records about the subscribing origin is unestablished.** The browser
  subscribes per origin, so Apple and Google are in a position to know the instance's domain, but
  neither documents what it stores. §6's "fragmentation" claim rests on an inference.
- **Delivery reliability was not researched.** Whether Web Push on a locked iPhone in Low Power
  Mode arrives in seconds or in an hour is unknown here and is a product-quality question, not a
  privacy one.
- **WhatsApp's push mechanism has no primary source** (§4). That its whitepaper is silent *is*
  established; that it uses an NSE is inference from observable behaviour.
- **Wire's iOS NSE was not verified in client source** — only implied by its server's own comment
  that the payload "gets overridden by the client". The payload quote itself is from source.
- **Apple publishes no budget for `mutable-content` alert pushes.** The two-or-three-per-hour
  figure is documented only for `content-available` background pushes. Claims of an NSE-specific
  budget circulate from developer-forum replies, not Apple documentation. Unestablished either
  way — do not design as though a limit exists, and do not assume one does not.
- **Google publishes no push-token request category** comparable to Apple's, as far as could be
  established — absence of evidence from secondary reporting, not a Google statement.
- **The native relay design was not designed.** §4 shows Zulip's bouncer proving a relay can be
  content-blind, and §5 establishes a relay is unavoidable for native; neither says what ours
  would look like, what it would cost to run, or who pays for it.
- **Quote fidelity caveat:** most sources were read through a summarising fetch layer. Apple's
  web-push and remote-notification-server pages, the UnifiedPush Android spec and the Matrix
  Push Gateway spec were read closely; quotes from ntfy, Mattermost, Rocket.Chat and the Android
  docs are near-verbatim and should be re-verified before being quoted in anything published.

## 8. The three questions the ADR must answer

Written as confirm-or-refute, per CLAUDE.md's pre-flight test.

**Q1 — Must the notification text be produced on the device, or may the server write it?**
Confirm or refute: because any server-composed notification is a channel PLAN §6.1's adversary 3
can forge and a place it demonstrates it holds the metadata E2EE was meant to minimise, **Strict
E2EE mode must produce notification text from decrypted MLS output on the device**, and a
server-written line is acceptable only in Compliance mode, where the server legitimately holds
plaintext. If refuted, say what a user is supposed to do with a notification the server could
have written, and note the corollary: if previews cannot be trusted then "New message" is the
only honest string Strict mode can show, and the question collapses to whether previews exist at
all rather than where they come from.

**Q2 — Can a service worker actually run the MLS core, on the devices that matter?**
Confirm or refute, **on real hardware and not by reading**: a push-only service worker on an iOS
16.4+ Home Screen web app and on Chrome for Android can instantiate the ~489 KB WASM core, open
the existing IndexedDB keystore through its non-extractable `CryptoKey`, take the
`navigator.locks` keystore lock against a live tab, decrypt one application message and display
a notification — inside whatever time and memory the platform grants a push handler. If it
cannot, options B and B′ are unavailable on the web, the recommendation below collapses to option
A, and Q1's answer decides whether Hamlaneh ships push at all before native apps exist.

**Q3 — What stops two contexts ratcheting the same MLS state?**
Confirm or refute: the service worker becomes the **single consumer of ciphertext**, writing
plaintext into the local plaintext store Phase 3 already plans, with `navigator.locks` serialising
it against any live tab — so decrypting inside a notification handler cannot consume a message
secret twice or leave the tab's epoch behind. If refuted, name the coordination that does hold,
because a design that ratchets in two places loses messages rather than merely annoying the user,
and that is the failure mode a notification feature must not introduce into a messaging product.

**Deliberately not a question here:** who runs the relay when native apps arrive. §5 establishes
that a relay is unavoidable and that the only real choice is who holds the metadata; PLAN §11
puts native mobile post-v1, so that row can be closed for the PWA and reopened, explicitly, when
native work starts. It is named so the next person reopens a known decision rather than
rediscovering a surprise.

## What this spike would recommend, if asked

Not a decision — the ADR makes it. On the evidence above: **Web Push, sent by the instance's own
Go server with its own self-generated VAPID keypair, with the notification text produced in a
push-only service worker after MLS decryption and a generic "New message" whenever decryption
cannot happen** (option B, with B′ as the payload shape if Q3 allows). It needs no Apple
Developer Program membership, no Firebase project and no vendor relationship of any kind, which
is the only design in this space that leaves "installation is the product" intact; it reaches
iOS Home Screen web apps and every Android browser; it gets UnifiedPush for free because
UnifiedPush is the same protocol; and it is the only option in which the notification a user
reads is attested by the same keys as the message it describes.

It also makes Wire's mistake structurally impossible rather than merely discouraged: on the web
path every byte of the payload is RFC 8291 ciphertext, so **no identifier can be left in it by
accident** — the class of bug that put a stable user UUID in Wire's pushes for years cannot be
written here. Q2 is what stands between that sentence and a real design, and Q2 can only be
answered on a phone.

## Sources

- Apple: [Sending notification requests to APNs](https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns) · [Pushing background updates to your app](https://developer.apple.com/documentation/usernotifications/pushing-background-updates-to-your-app) · [Modifying content in newly delivered notifications](https://developer.apple.com/documentation/usernotifications/modifying-content-in-newly-delivered-notifications) · [Sending web push notifications in web apps and browsers](https://developer.apple.com/documentation/usernotifications/sending-web-push-notifications-in-web-apps-and-browsers) · [Setting up a remote notification server](https://developer.apple.com/documentation/usernotifications/setting-up-a-remote-notification-server)
- WebKit: [Web Push for Web Apps on iOS and iPadOS](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/) · [Meet Web Push](https://webkit.org/blog/12945/meet-web-push/) · [Full Third-Party Cookie Blocking and More](https://webkit.org/blog/10218/full-third-party-cookie-blocking-and-more/)
- Firebase: [About FCM messages](https://firebase.google.com/docs/cloud-messaging/concept-options) · [Understanding message delivery](https://firebase.google.com/docs/cloud-messaging/understand-delivery) · [FCM Android client](https://firebase.google.com/docs/cloud-messaging/android/client)
- Android: [Optimize for Doze and App Standby](https://developer.android.com/training/monitoring-device-state/doze-standby) · [Behavior changes: Android 14](https://developer.android.com/about/versions/14/behavior-changes-14) · [Foreground service and full-screen intent requirements](https://support.google.com/googleplay/android-developer/answer/13392821)
- IETF: [RFC 8030 — Generic Event Delivery Using HTTP Push](https://www.rfc-editor.org/rfc/rfc8030.html) · [RFC 8291 — Message Encryption for Web Push](https://www.rfc-editor.org/rfc/rfc8291.html) · [RFC 8292 — VAPID](https://www.rfc-editor.org/rfc/rfc8292.html)
- MDN: [Push API](https://developer.mozilla.org/en-US/docs/Web/API/Push_API) · [PushManager.subscribe](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe) · [Web Locks API](https://developer.mozilla.org/en-US/docs/Web/API/Web_Locks_API) · Chrome: [Web Push Interoperability Wins](https://developer.chrome.com/blog/web-push-interop-wins)
- Signal: [`APNSender.java`](https://github.com/signalapp/Signal-Server/blob/main/service/src/main/java/org/whispersystems/textsecuregcm/push/APNSender.java) · [`FcmSender.java`](https://github.com/signalapp/Signal-Server/blob/main/service/src/main/java/org/whispersystems/textsecuregcm/push/FcmSender.java) · [`SignalNSE/NotificationService.swift`](https://github.com/signalapp/Signal-iOS/blob/main/SignalNSE/NotificationService.swift) · [`FcmReceiveService.java`](https://github.com/signalapp/Signal-Android/blob/main/app/src/main/java/org/thoughtcrime/securesms/gcm/FcmReceiveService.java)
- Wire: [`Gundeck/Push/Native/Serialise.hs`](https://github.com/wireapp/wire-server/blob/develop/services/gundeck/src/Gundeck/Push/Native/Serialise.hs) · [wire-server#549](https://github.com/wireapp/wire-server/pull/549) · [infrastructure-configuration.md](https://github.com/wireapp/wire-server/blob/develop/docs/src/how-to/install/infrastructure-configuration.md) · [Wire Privacy Whitepaper (Jan 2025)](https://wire.com/hubfs/wire-white-privacy-paper.pdf) · [Wire Security Whitepaper (May 2025)](https://wire.com/hubfs/Whitepapers/-wire-security-whitepaper.pdf)
- Matrix: [Push Gateway API](https://spec.matrix.org/latest/push-gateway-api/) · [element-hq/sygnal `applications.md`](https://github.com/element-hq/sygnal/blob/main/docs/applications.md) · [Element — own push gateway](https://docs.element.io/latest/element-server-suite-classic/advanced-configuration/notifications-mdm-push-gateway/)
- Zulip: [Mobile push notification service](https://zulip.readthedocs.io/en/stable/production/mobile-push-notifications.html) · WhatsApp: [Encryption Overview whitepaper](https://www.whatsapp.com/security/WhatsApp-Security-Whitepaper.pdf)
- Measurement: Samarin et al., *The Medium is the Message: How Secure Messaging Apps Leak Sensitive Data to Push Notification Services*, [PETS 2024](https://arxiv.org/abs/2407.10589)
- UnifiedPush: [Android spec](https://unifiedpush.org/developers/spec/android/) · [FAQ](https://unifiedpush.org/users/faq/) · [Distributors](https://unifiedpush.org/users/distributors/) · [embedded_fcm_distributor](https://github.com/UnifiedPush/android-embedded_fcm_distributor) · [element-android UnifiedPush docs](https://github.com/element-hq/element-android/blob/develop/docs/unifiedpush.md)
- ntfy: [Subscribe from your phone](https://docs.ntfy.sh/subscribe/phone/) · [Configuring the server](https://docs.ntfy.sh/config/)
- Mattermost: [Host your own push proxy service](https://docs.mattermost.com/deployment-guide/mobile/host-your-own-push-proxy-service.html) · [Mobile FAQ](https://docs.mattermost.com/deployment-guide/mobile/mobile-faq.html) · Rocket.Chat: [Push notifications](https://docs.rocket.chat/docs/push-notifications)
- Push notification surveillance: [Wyden letter to DOJ, 6 Dec 2023](https://www.wyden.senate.gov/imo/media/doc/wyden_smartphone_push_notification_surveillance_letter.pdf) · [Apple Legal Process Guidelines, US](https://www.apple.com/legal/privacy/law-enforcement-guidelines-us.pdf) · [Apple transparency — push token requests](https://www.apple.com/legal/transparency/push-token.html) · [TechCrunch](https://techcrunch.com/2023/12/06/us-senator-warns-governments-spying-apple-google-smartphone-users-via-push-notifications/) · [EPIC](https://epic.org/sen-wyden-reveals-government-surveillance-of-smartphone-push-notifications/)
- Apple reverses EU Home Screen web app removal: [TechCrunch](https://techcrunch.com/2024/03/01/apple-reverses-decision-about-blocking-web-apps-on-iphones-in-the-eu/) · [9to5Mac](https://9to5mac.com/2024/03/01/apple-home-screen-web-apps-ios-17-eu/)
- In-repo: [ADR 006](../adr/006-mls-library-and-boundaries.md) · [spike: MLS WASM integration](mls-wasm-integration.md) · `webapp/src/mls/keystore.ts` · `webapp/public/brand/manifest.json` · `server/internal/storage/messages.go`
