-- Message search, and the text-search decision migration 0003 deferred to
-- the code that has to live with it.
--
-- THE DECISION: trigram substring matching (pg_trgm), NOT tsvector/FTS.
--
-- PostgreSQL's full-text search is built around a per-language
-- configuration: a parser, a stemmer and a stop-word list chosen when the
-- index is created. That works when an instance speaks one language. This
-- product is bilingual English/Persian from the first screen, one channel
-- can hold both languages in adjacent messages, and PostgreSQL ships no
-- Persian configuration at all. The choices FTS actually offered were:
--
--   * to_tsvector('english', content): English gets real stemming; Persian
--     gets whole-word matching only, because the English Snowball stemmer's
--     rules are ASCII suffixes and leave Persian tokens untouched. Half the
--     users would get a search that cannot find كتاب inside كتاب‌ها.
--   * to_tsvector('simple', content): whole-word matching for both, no
--     stemming for either — the English half of the product downgraded to
--     buy nothing for the Persian half.
--   * a per-message language column: needs reliable language detection of
--     50-character chat messages that routinely mix scripts. It does not
--     exist, and guessing wrong is a message that can never be found.
--
-- So: substring matching over a normalized copy of the text, accelerated by
-- a GIN trigram index. It treats both languages identically, it needs no
-- dictionary, and it is what the contract's snippet shape already implies —
-- SearchSnippet marks "the run the query matched" (openapi.yaml), and only a
-- literal substring match can point at the characters the user typed. A
-- stemmed match would highlight nothing, because the matched stem is not in
-- the message.
--
-- WHAT THIS BUYS AND WHAT IT COSTS, stated plainly:
--   * Both languages: any substring of a message is findable, including
--     inside a word. "deploy" finds "deploying"; كتاب finds كتاب‌ها.
--   * Neither language gets stemming. "deploying" does NOT find "deploy",
--     and رفتم does NOT find می‌رود. Search matches characters, not meaning.
--   * Substrings cross word boundaries, so "cat" matches "concatenate".
--     There is no relevance ranking to push such a hit down: results are
--     ordered newest-first, which is what a chat search wants anyway.
--   * A needle shorter than three characters cannot use the index (a
--     trigram is three characters) and degrades to a sequential scan. The
--     contract permits q as short as one character, so this is a real cost
--     on a large instance; see the query in storage/search.go.
--
-- THE NORMALIZATION, and why it is part of the decision. Persian text is
-- typed with characters that are visually identical and different code
-- points, so a naive substring search fails for users on the wrong keyboard
-- layout. translate() folds the three that matter and drops the zero-width
-- non-joiner, so كتاب (Arabic kaf) and کتاب (Persian keheh) are one needle,
-- and کتاب‌ها (with ZWNJ) is found by کتابها (without). Written as Unicode
-- escapes on purpose: one of these characters is invisible, and a reviewer
-- must be able to see what the expression maps.
--
--     U+064A ARABIC LETTER YEH          -> U+06CC ARABIC LETTER FARSI YEH
--     U+0643 ARABIC LETTER KAF          -> U+06A9 ARABIC LETTER KEHEH
--     U+0629 ARABIC LETTER TEH MARBUTA  -> U+0647 ARABIC LETTER HEH
--     U+200C ZERO WIDTH NON-JOINER      -> removed (no counterpart in `to`)
--
-- Deliberately NOT folded: Arabic-Indic and Persian digits (٠ vs ۰ vs 0),
-- alef variants (أ إ آ), and diacritics. Each is a judgement call about
-- Persian orthography rather than a keyboard artifact, and folding is not
-- free — it is applied to the query too, so an over-eager map makes distinct
-- words unsearchable. This set can be widened later, but only by REPLACING
-- this index in a new migration: the folding lives inside the index
-- expression, so changing it is changing what is indexed. storage/search.go
-- carries the matching Go implementation and a test that pins the two to
-- each other.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Partial on deleted_at: a soft-deleted message has its content erased to ''
-- (constraint messages_content_shape), so it can never match a non-empty
-- needle. Keeping those rows out of the index makes that a property of the
-- schema rather than a filter a future query could forget — and the search
-- query states the same predicate so the planner can use this index.
CREATE INDEX messages_content_search_idx
    ON messages
    USING gin (
        translate(content, U&'\064A\0643\0629\200C', U&'\06CC\06A9\0647')
        gin_trgm_ops
    )
    WHERE deleted_at IS NULL;
