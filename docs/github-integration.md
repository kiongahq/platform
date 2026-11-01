# GitHub repository connections

GitHub integration connects a personal GitHub account to the platform. Users can
discover accessible repositories, connect one to an existing project, or import
selected repositories as project records. Synchronization updates repository
URLs and default branches; it does not push code, clone source into a workspace,
or grant platform access outside the caller's existing permissions.

## Connect an account

In Settings, follow **Connect GitHub account** when a GitHub App is configured.
A fine-grained personal token is also supported. Grant only the repository read
access needed for discovery. GitHub credentials are encrypted with
`KIONGA_CREDENTIAL_KEY`, stored per platform subject and never returned to the
browser or passed through a workspace proxy. Disconnecting removes the saved
credential from the platform; GitHub authorization can also be revoked in GitHub.

## Configure an App

Register a GitHub App with repository metadata and content read permissions. The
installation can cover all repositories or selected repositories. Configure these
gateway variables:

| Variable | Purpose |
| --- | --- |
| `KIONGA_GITHUB_APP_SLUG` | App slug used in its installation URL |
| `KIONGA_GITHUB_APP_CLIENT_ID` | GitHub App OAuth client ID |
| `KIONGA_GITHUB_APP_CLIENT_SECRET` | GitHub App OAuth client secret |
| `KIONGA_GITHUB_APP_CALLBACK_URL` | Console origin plus `/api/v1/settings/github/callback` |
| `KIONGA_CREDENTIAL_KEY` | Base64-encoded 32-byte account encryption key |

Set the App's OAuth callback URL to the same callback. Its installation setup URL
can point to the console origin plus `/console/settings`. The callback must use
the same browser origin as the signed-in console, including the local-development
hostname and port. These App settings are separate from the Dex GitHub identity
provider's `KIONGA_GITHUB_CLIENT_ID` and `KIONGA_GITHUB_CLIENT_SECRET`.

Use **Install GitHub App**, choose repository access in GitHub, then refresh App
installations in Settings and select the installation. The gateway verifies that
the installation is accessible to the connected GitHub user and belongs to the
configured App. An installation ID alone does not authorize access. OAuth state
is encrypted, expires after ten minutes and is bound to the platform subject.
When a GitHub user authorization expires, reconnect the account.

## Synchronize project records

Choose repositories and select **Sync selected**. Existing visible project
connections are matched by normalized repository URL; new repositories create
projects using the standard project API. Every update and creation is subject
to existing project/Git permissions, policy checks and resource limits. Failures
are reported per repository, and successful imports remain visible. A synchronization
is an explicit operation; no periodic import or GitHub webhook is configured.
Repository discovery is bounded to 1,000 repositories per connection. Larger
accounts should install the App on a selected set of repositories.

For an individual project, use **Connect repository** and choose from GitHub or
enter its repository URL and default branch manually. Checkout and source changes
remain actions performed through the project's workspace and Git tooling.
