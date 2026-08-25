package storage

// RegisterCitext exposes registerCitext to the external integration tests,
// which pin its error contract against a real PostgreSQL.
var RegisterCitext = registerCitext

// ParseMentions exposes the mention parser to the external tests, which fuzz
// it, and MentionTokenLen the exact byte width of one token, which bounds how
// many mentions any content can hold.
var ParseMentions = parseMentions

// MentionTokenLen is mentionTokenLen for those tests.
const MentionTokenLen = mentionTokenLen
