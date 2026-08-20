import { useTranslation } from "react-i18next";

import { LanguageSwitcher } from "./components/LanguageSwitcher";
import { LoginScreen } from "./screens/LoginScreen";

function App() {
  const { t } = useTranslation();

  return (
    <>
      <header>
        <h1>{t("app.name")}</h1>
        <p>{t("app.tagline")}</p>
        <LanguageSwitcher />
      </header>
      <main>
        <LoginScreen />
      </main>
    </>
  );
}

export default App;
