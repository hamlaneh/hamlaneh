-- Nothing to salvage: invites are credentials, settings have defaults the
-- code carries anyway, and the audit log is a record this schema version
-- cannot hold. Rolling back a feature deletes the feature's own data; it
-- does not touch anything the earlier schema was already responsible for.
DROP TABLE audit_entries;
DROP TABLE org_settings;
DROP TABLE invites;
