-- Enforcement for org_settings.require_totp, which has been stored and
-- editable since 0009 and read by nothing.
--
-- The admin turns on "require two-step verification", the screen agrees, and
-- the instance does not change: no code outside the admin handlers ever
-- consults the setting. A security control that does nothing is worse than an
-- absent one, because it is believed.
--
-- The flag lives on the session rather than being computed from the setting
-- on each request, and that is what makes the contract's promise -- "at the
-- next sign-in, never mid-session" -- true rather than aspirational. Deciding
-- it live would strand every session open at the moment the policy is flipped,
-- starting with the administrator who flipped it.
--
-- DEFAULT false is the only defensible backfill: no session that exists today
-- was minted under an enforced policy, and a migration must not lock anybody
-- out of a session they are in the middle of.
ALTER TABLE sessions
    ADD COLUMN totp_enrollment_required boolean NOT NULL DEFAULT false;
