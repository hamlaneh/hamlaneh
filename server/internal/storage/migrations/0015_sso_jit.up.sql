-- Whether an identity the provider vouches for, matching no account here,
-- creates one.
--
-- DEFAULT false is the point rather than a cautious default. While it is off
-- the account-creating branch does not run at all, which is what makes
-- "single sign-on cannot walk around registration being closed" true by
-- construction rather than by a check that could be wrong.
--
-- Deliberately its own column rather than something read off
-- registration_mode: that setting governs a self-serve password door, and an
-- administrator configuring an identity provider is not the same consent as
-- letting everybody in that directory have an account here.
ALTER TABLE org_settings
    ADD COLUMN sso_jit_provisioning boolean NOT NULL DEFAULT false;
