package storage

// RegisterCitext exposes registerCitext to the external integration tests,
// which pin its error contract against a real PostgreSQL.
var RegisterCitext = registerCitext
