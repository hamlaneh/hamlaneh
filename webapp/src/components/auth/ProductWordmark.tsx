import { useTranslation } from "react-i18next";

/** Product name and the single supporting line, per the delivered design. */
export function ProductWordmark() {
  const { t } = useTranslation();

  return (
    <>
      <h1 className="hm-wordmark">{t("app.name")}</h1>
      <p className="hm-tagline">{t("app.tagline")}</p>
    </>
  );
}
