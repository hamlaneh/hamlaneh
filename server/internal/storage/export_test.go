package storage

// RegisterCitext exposes registerCitext to the external integration tests,
// which pin its error contract against a real PostgreSQL.
var RegisterCitext = registerCitext

// ParseMentions, MentionTokenLen, FoldSearchText and SearchSnippet used to be
// exported here for the tests alone. They are ordinary exports of the package
// now (mentions.go, search.go), because home mode's SQLite driver reuses all
// four: the mention wire format and the search fold are product decisions, and
// a second copy of either would be a second decision.

// SearchPageQuery is the search statement the external test runs EXPLAIN on.
// That test is the only thing standing between "the trigram index is used"
// and a silent sequential scan, so the statement has to be the real one and
// not a copy that could drift from it.
const SearchPageQuery = searchPageQuery

// FileSearchPageQuery is the same guard for the filename half: the real
// statement, so the EXPLAIN test cannot pass against a copy that has drifted
// from migration 0007's index expression.
const FileSearchPageQuery = fileSearchPageQuery

// NormalizedFilename is the folded-filename expression migration 0007
// indexes. The test EXPLAINs it on its own, because whether the PLANNER
// prefers that index over the message_id one is a cost decision that moves
// with table size — what must never drift is the expression itself.
const NormalizedFilename = normalizedFilename
