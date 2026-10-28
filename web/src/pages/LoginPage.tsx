import { useSearchParams } from "react-router";
import emptyEyes from "../../../assets/states/empty.svg";
import { ButtonLink } from "../components/Button";
import { safeReturnPath, serverLoginUrl } from "../routes";
import "./LoginPage.css";

/** The page for a visitor without a session: one Sign in button that starts the login at the server. */
export function LoginPage() {
  const [params] = useSearchParams();
  const target = safeReturnPath(params.get("return"));

  return (
    <main className="sk-login">
      <div className="sk-login__card">
        <img src={emptyEyes} alt="" width={96} height={96} />
        <h1 className="sk-login__title">Sidekick</h1>
        <p className="sk-login__text">Sign in with your organization's account to see investigations.</p>
        <ButtonLink variant="brand" href={serverLoginUrl(target)}>
          Sign in
        </ButtonLink>
      </div>
    </main>
  );
}
