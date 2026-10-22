# Import a GitHub repository

Open **Add project** and choose **GitHub**. Deck uses the GitHub CLI installed
on the Deck machine. Choose **Connect GitHub** if no account is connected; the
picker then lists your repositories. Your browser machine does not need `gh`.
See [Connect GitHub](github-connection.md) for account setup and native credential
ownership.

The picker lists repositories accessible to the active GitHub.com account,
including repositories owned by collaborators and organizations. Search and
the owner filter cover the completed paginated listing. If enumeration hits
a time, size, or rate limit, Deck reports the incomplete read instead of
presenting a partial list as complete. Refresh explicitly after changing
account permissions. A missing repository does not establish why access is
unavailable; organization SSO and repository permissions remain GitHub's.
This imports repositories, not GitHub Projects boards. The native command uses
GitHub’s [authenticated-user repository endpoint](https://docs.github.com/en/rest/repos/repos#list-repositories-for-the-authenticated-user)
through [`gh api`](https://cli.github.com/manual/gh_api).

Choose **Import**, review the destination and Git author identity, then start
the clone. Account access and commit authorship are separate: a GitHub login
does not replace the selected local Git author profile. Deck rechecks the
account and repository before starting. A moved or renamed repository requires
a fresh selection. Existing registered repositories offer **Open**.

Clones use the existing Git job and the machine's native credential helpers.
Private HTTPS access therefore requires the native GitHub CLI credential
helper setup to work on that installation. Deck does not write global helper
configuration when the picker opens. The native GitHub login may offer that
setup as an explicit choice.

Closing the dialog keeps an active clone running. **Cancel import** leaves its
partial copy available for inspection or removal. Trying the clone again
restarts the fetch; it does not resume at a byte offset. If GitHub access cannot
be revalidated, the partial copy is retained. No repository setup command,
agent session, or model prompt is run by importing a repository. A PRD is not
required.

**Local folder** and **Repository URL** remain available for existing folders,
new folders, and other Git hosts. **Start with the tutorial project** opens the
first tutorial lesson, which works on the relnotes and ccparse practice
projects. Opening the lesson creates no project or provider session; its setup
actions each require an explicit choice.

## Qualification

Automated tests exercise synthetic owned, collaborator, and organization
repositories, pagination, access changes, and temporary clone jobs. They do not
establish that a real account's SSO, native helper, or private HTTPS clone works.
Before evaluator release, approved-account qualification is required on
the exact runtime and candidate revision, including fresh native login,
persisted credentials, native helper setup and real owned, collaborator and
organization private HTTPS imports. Record the evaluator rehearsal results.
Do not record tokens or authentication output in that evidence.
