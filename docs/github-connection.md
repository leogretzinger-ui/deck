# Connect GitHub

Choose **Connect GitHub** during Git identity setup or when you import a
project from GitHub. You can also choose **GitHub connection…** from the
global three-dot menu. You can connect before you create a project or save a
Git author identity.

If no account is connected, opening the screen starts sign-in. Deck runs the
installed GitHub CLI in its own runtime, so you do not need the GitHub CLI on
the computer running your browser. When the CLI prints a one-time code, Deck
shows it. Choose **Copy code and open GitHub**, paste the code on the page
that opens, and approve the sign-in. The screen changes to the connected
account by itself. Choose **Done** to close it.

If an account is already connected, the screen shows it and starts nothing.
To use a different account, run `gh auth logout` where Deck runs, then connect
again.

Choose **Show details** to see what the GitHub CLI printed and to answer its
prompts. If another browser controls the sign-in, choose **Take control** to
continue in this one. If the terminal disconnects, choose **Reconnect**.

Closing the screen leaves the sign-in running. Reopen it to return to the same
sign-in. **Cancel** stops only the process that belongs to this sign-in. The
screen then shows your connection as it is now: connected, or ready to connect
again. If the sign-in ends before GitHub approves it, choose **Try again** to
start a new one. Deck never repeats a start or a cancel whose outcome it could
not confirm. It reads the connection again and shows "Checking your GitHub
connection…" until the answer arrives. If Deck cannot confirm that a
sign-in started, it blocks another sign-in until Deck restarts. Check the
runtime before you restart Deck. A restart ends the link to any running
sign-in; Deck does not adopt or stop old sign-in processes by name.

A healthy active account is preserved. A failed, stored active credential can
be reconnected explicitly. Environment credentials such as `GH_TOKEN` retain
the GitHub CLI's normal precedence and cannot be replaced by this dialog;
change them in your deployment. Unknown credential sources and ambiguous
status require attention through the native CLI. Git author identity remains
separate from GitHub authentication.

This flow supports GitHub.com and requires authenticated Deck access. An
installation explicitly using `auth.mode: none` must enable an authenticated
mode before using this principal-bound flow. Organization SSO and repository
access remain governed by the account's GitHub permissions; an absent or
inaccessible repository does not establish why access was denied.

Real account consent, organization SSO, credential persistence, fresh helper
setup and new private HTTPS clones remain qualification gates you run until
recorded against the deployed candidate. Automated tests use isolated tmux
sockets and synthetic CLI fixtures; they do not exercise your credentials.
