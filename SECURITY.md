# Security Policy

## Reporting a vulnerability

Please use GitHub's **Report a vulnerability** form in this repository's Security tab. Reports submitted there are private.

Do not disclose suspected vulnerabilities, credentials, private handoff links, or user data in a public issue.

Include the affected version, reproduction steps, impact, and any suggested mitigation. The maintainers will acknowledge the report and coordinate remediation and disclosure through the private report.

## Security model

- A handoff URL or share code is a read capability. Anyone who receives it can read that handoff until it expires or is deleted.
- Anonymous publishing and receiving do not require login.
- Server-side compaction requires an active OpenGrove login.
- Per-handoff delete credentials are stored locally by the creator; the service stores only their hashes.
- Secrets and production credentials must be supplied through deployment secrets or local configuration and must never be committed.
