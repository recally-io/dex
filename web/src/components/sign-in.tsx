import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { createSessionMutation } from "../api/generated/@tanstack/react-query.gen";
import { Button } from "./atoms/Button";
import { Field, Input } from "./form";
import { FramedPage, Section } from "./page";
import { errorText } from "../lib/queries";

/** Shown when dex requires the access token. A correct token sets the session cookie. */
export function SignIn() {
  const [token, setToken] = useState("");
  const signIn = useMutation({ ...createSessionMutation(), onSuccess: () => window.location.reload() });
  const error = signIn.error ? errorText(signIn.error) : undefined;

  return (
    <FramedPage
      title="Sign in to dex"
      description="This dex accepts network connections, so it requires its access token."
    >
      <Section
        n={1}
        title="Access token"
        caption="On the dex host, run dex token to print it. If you started dex with DEX_TOKEN, use that token."
      >
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            signIn.mutate({ body: { token: token.trim() } });
          }}
        >
          <Field label="Token" error={error}>
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
            <Button size="sm" type="submit" disabled={signIn.isPending || token.trim() === ""}>
              Sign in
            </Button>
          </div>
        </form>
      </Section>
    </FramedPage>
  );
}
