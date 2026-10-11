# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub: open this repository's **Security** tab and choose **Report a vulnerability** (GitHub private vulnerability reporting). Don't open a public issue.

Include what you found, how to reproduce it and what impact you expect. We'll acknowledge the report, keep you updated and credit you in the advisory unless you'd rather stay anonymous.

## Supported versions

meta is in early development. Only the latest commit on `main` gets fixes.

## Threat model in short

- **Plugins are separate programs.** meta runs managed plugins as child processes and talks to remote plugins over the network. Only add plugins you trust.
- **API keys** for the meta API and for remote plugins come from configuration. They are never logged and never returned by the API.
- **Database:** meta needs only its own database. It never needs a PostgreSQL superuser and changes no server-wide settings.
