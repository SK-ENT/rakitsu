# Release Notes

Rakitsu is licensed under the [Business Source License 1.1](LICENSE). Per the
license text, the **Change Date** — the point at which a given version's
source converts to the Change License (Apache 2.0) — is four years from the
date *that specific version* was first publicly distributed, not a single
fixed date for the whole project:

> "Change Date: Four years from the date each version of the Licensed Work
> is first publicly distributed."
>
> "This License applies separately for each version of the Licensed Work and
> the Change Date may vary for each version..."

This file is the durable, git-tracked record of that date for each release —
independent of GitHub's own release metadata, which is useful but mutable
(a release can be deleted and recreated, changing its timestamps).

Timestamps are recorded to the second, in UTC, not just a calendar date — a
bare date is ambiguous once a reader is in a different timezone than the one
that first comes to mind (this project's own maintainers are UTC+9, where a
release published late in the UTC day already falls on the next calendar
day locally).

Newest first. Once the table below holds more than 5 releases, move the
oldest rows into the collapsed "Older releases" section beneath it (also
newest-first there) rather than letting this table grow without bound —
GitHub's find-in-page still searches inside a collapsed `<details>` block,
so no row becomes harder to actually locate, just less prominent by default.

| Version | First published (UTC) | Converts to Apache 2.0 (UTC) |
|---|---|---|
| [v0.3.0-alpha.6](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.0-alpha.6) | 2026-09-19T22:17:15Z | 2030-09-19T22:17:15Z |
| [v0.3.0-alpha.5](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.0-alpha.5) | 2026-09-13T12:36:43Z | 2030-09-13T12:36:43Z |
| [v0.3.0-alpha.4](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.0-alpha.4) | 2026-09-13T03:29:03Z | 2030-09-13T03:29:03Z |
| [v0.3.0-alpha.3](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.0-alpha.3) | 2026-09-12T11:44:27Z | 2030-09-12T11:44:27Z |
| [v0.3.0-alpha.2](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.0-alpha.2) | 2026-09-12T08:02:34Z | 2030-09-12T08:02:34Z |
| [v0.3.0-alpha.1](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.0-alpha.1) | 2026-09-11T14:32:37Z | 2030-09-11T14:32:37Z |

<details>
<summary>Older releases</summary>

| Version | First published (UTC) | Converts to Apache 2.0 (UTC) |
|---|---|---|
| [v0.2.0-alpha.5](https://github.com/SK-ENT/rakitsu/releases/tag/v0.2.0-alpha.5) | 2026-09-08T01:26:19Z | 2030-09-08T01:26:19Z |
| [v0.2.0-alpha.4](https://github.com/SK-ENT/rakitsu/releases/tag/v0.2.0-alpha.4) | 2026-09-06T10:25:42Z | 2030-09-06T10:25:42Z |
| [v0.2.0-alpha.3](https://github.com/SK-ENT/rakitsu/releases/tag/v0.2.0-alpha.3) | 2026-09-06T06:36:17Z | 2030-09-06T06:36:17Z |
| [v0.2.0-alpha.2](https://github.com/SK-ENT/rakitsu/releases/tag/v0.2.0-alpha.2) | 2026-09-06T02:07:06Z | 2030-09-06T02:07:06Z |
| [v0.2.0-alpha.1](https://github.com/SK-ENT/rakitsu/releases/tag/v0.2.0-alpha.1) | 2026-09-05T23:26:49Z | 2030-09-05T23:26:49Z |

</details>

Add a new row at the **top** of the table above whenever a new tag is
released. Use the release's own `published_at` timestamp — `gh release view
<tag> --json publishedAt`, or the GitHub UI — as the "First published" value,
not the day this file happens to be edited, which may not be the same day.
If a release is ever deleted and recreated (as happened once during
`v0.2.0-alpha.1`'s own initial setup), update the row to the *recreated*
release's timestamp — that's the one that actually counts as "first publicly
distributed".
