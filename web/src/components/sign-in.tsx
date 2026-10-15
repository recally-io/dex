import { useState, type FormEvent } from "react";
import { createSession } from "../api/generated/sdk.gen";
import { Button } from "./atoms/Button";
import { Field, Input } from "./form";
import { FramedPage, Section } from "./page";
import { errorText } from "../lib/queries";

/** Shown when dex requires the access token. A correct token sets the session cookie. */
export function SignIn() {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    const result = await createSession({ body: { token: token.trim() } });

    if (result.error === undefined && result.response?.ok) {
      window.location.reload();

      return;
    }

    setBusy(false);
    setError(result.error === undefined ? "Sign-in failed. Try again." : errorText(result.error));
  }

  return (
    <FramedPage
      title="Sign in to dex"
      description="This dex accepts network connections, so it requires its access token."
    >
      <Section n={1} title="Access token" caption="On the dex host, run dex token to print it.">
        <form className="flex flex-col gap-3" onSubmit={(event) => void submit(event)}>
          <Field label="Token" error={error || undefined}>
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid || undefined}
                type="password"
                autoComplete="current-password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                required
              />
            )}
          </Field>
          <div>
            <Button size="sm" type="submit" disabled={busy || token.trim() === ""}>
              Sign in
            </Button>
          </div>
        </form>
      </Section>
    </FramedPage>
  );
}
