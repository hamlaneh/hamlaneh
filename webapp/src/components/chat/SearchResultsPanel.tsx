import { useTranslation } from "react-i18next";
import { Link } from "react-router";

import { formatCount, formatResultStamp } from "../../chat/format";
import type { SearchKind, SearchState } from "../../chat/store";
import type { ChannelRef, SearchResult } from "../../chat/types";
import { XIcon } from "../icons";
import { Avatar } from "./Avatar";

interface SearchResultsPanelProps {
  search: SearchState;
  onClose: () => void;
  onSelectKind: (kind: SearchKind) => void;
}

function channelLabel(channel: ChannelRef): string {
  return channel.kind === "dm"
    ? (channel.dm_peer?.display_name ?? "")
    : `#${channel.slug ?? ""}`;
}

function ResultCard({ result }: { result: SearchResult }) {
  const { i18n } = useTranslation();
  return (
    <li>
      <Link className="hm-result" to={`/c/${result.channel.id}/m/${result.message_id}`}>
        <span className="hm-result__top">
          <span className="hm-result__channel" dir={result.channel.kind === "dm" ? "auto" : "ltr"}>
            {channelLabel(result.channel)}
          </span>
          <span>{formatResultStamp(result.created_at, i18n.language)}</span>
        </span>
        <span className="hm-result__author">
          <Avatar
            userId={result.author.id}
            displayName={result.author.display_name}
            size={20}
            typeSize={8}
          />
          {result.author.display_name}
        </span>
        {/* Snippets arrive as {text, match} parts — never HTML — so a message
            body can never inject markup through a search result. */}
        <span className="hm-result__snippet" dir="auto">
          {result.snippet.parts.map((part, index) =>
            part.match ? (
              <mark key={index}>{part.text}</mark>
            ) : (
              <span key={index}>{part.text}</span>
            ),
          )}
        </span>
      </Link>
    </li>
  );
}

/**
 * Search as a third column beside the conversation, so the conversation stays
 * in place. Below 1280 the same panel floats over the message list instead.
 */
export function SearchResultsPanel({ search, onClose, onSelectKind }: SearchResultsPanelProps) {
  const { t, i18n } = useTranslation();
  if (search.status === "closed") {
    return null;
  }

  const results = search.status === "ready" ? search.page.results : [];
  const total = search.status === "ready" ? search.page.total : 0;
  const capped = search.status === "ready" && search.page.total_capped;

  const title = capped
    ? t("chat.search.resultsCapped", {
        count: formatCount(total, i18n.language),
        query: search.query,
      })
    : total === 1
      ? t("chat.search.resultsOne", { query: search.query })
      : t("chat.search.resultsOther", {
          count: formatCount(total, i18n.language),
          query: search.query,
        });

  return (
    <aside className="hm-search-panel" aria-label={t("chat.search.label")}>
      <div className="hm-search-panel__header">
        <h2 className="hm-search-panel__title">
          {search.status === "loading" ? t("chat.search.searching") : title}
        </h2>
        <button
          type="button"
          className="hm-icon-button"
          onClick={onClose}
          aria-label={t("chat.search.close")}
        >
          <XIcon size={17} strokeWidth={1.85} />
        </button>
      </div>

      {/* Two filters over one list, not tabs: a tablist promises arrow-key
          navigation and a tabpanel this panel does not have. A labelled group
          of toggles is the complete pattern for what this actually is. */}
      <div className="hm-search-panel__tabs" role="group" aria-label={t("chat.search.kindLabel")}>
        <button
          type="button"
          className="hm-search-tab"
          aria-pressed={search.kind === "messages"}
          onClick={() => {
            onSelectKind("messages");
          }}
        >
          {t("chat.search.messages")}
        </button>
        <button
          type="button"
          className="hm-search-tab"
          aria-pressed={search.kind === "files"}
          onClick={() => {
            onSelectKind("files");
          }}
        >
          {t("chat.search.files")}
        </button>
      </div>

      {search.status === "error" ? (
        <p className="hm-search-panel__empty">{t("chat.search.failed")}</p>
      ) : search.status === "ready" && results.length === 0 ? (
        <p className="hm-search-panel__empty">
          {/* `files` is accepted by the contract but empty until the Phase 1.3
              upload pipeline exists — say so rather than showing "no results". */}
          {search.kind === "files" ? t("chat.search.filesPending") : t("chat.search.noResults")}
        </p>
      ) : (
        <ul className="hm-search-panel__results">
          {results.map((result) => (
            <ResultCard key={result.message_id} result={result} />
          ))}
        </ul>
      )}
    </aside>
  );
}
