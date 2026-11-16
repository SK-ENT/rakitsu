# Sort Control certification record

**Decision: production certification pending.** Rakitsu's repaired output passed the seven automated Chrome checks. Visual approval, full accessibility review, and production integration remain open.

| ID | Empty memory | Learned procedure | Repaired | Evidence |
| --- | --- | --- | --- | --- |
| VIS-01 | Unverified | Unverified | Unverified | Desktop captures visually preserve the reference hierarchy, colors, copy, and split queue/detail. The learned version adds a visible filter label and shifts the queue down. Product/UX visual approval is pending. |
| REU-01 | Pass | Pass | Pass | All candidates load the kit JS and CSS and use registered queue, metric, and drawer elements. |
| UX-01 | Pass | Pass | Pass | Initial lane, metrics, and required text. |
| UX-02 | Pass | Pass | Pass | Case-insensitive filtering, empty state, retained selection, detail updates, and running-lane action visibility. |
| UX-03 | Pass | Pass | Pass | Correct modal record, Escape and Cancel without save, scoped Save, count change, focus return or successor, and note retained on lane switch. |
| A11Y-01 | Fail | Fail | Pass | Native dialogs returned document focus to BODY during Tab and Shift+Tab cycling in the originals. Rakitsu added wrap behavior using the existing dialog, and the browser verified both directions and Escape focus return. |
| A11Y-02 | Unverified | Unverified | Unverified | Full contrast, zoom, screen-reader, and motion review pending. |
| RESP-01 | Pass | Pass | Pass | Chrome desktop and 390px layout checks and screenshots. |
| ROB-01 | Pass | Pass | Pass | No page exceptions during the exercised flows. The test note appears literally and creates no injected element. |
| PROD-01 | Unverified | Unverified | Unverified | The experiment uses synthetic records and session state. Live API, durable writes, permissions, retry/error handling, and duplicate submission behavior still need integration testing. |
| PROD-02 | Unverified | Unverified | Unverified | Release browser/device matrix, performance budget, monitoring, and owner sign-off pending. |

The automated passes cover the exercised flows. For example, keyboard cycling, Escape, and named dialog behavior were exercised in Chrome. The complete accessibility and release checks require the additional evidence above.

## Experiment evidence

- [Reference image](reference/sort-control.png) and [UI/UX spec](reference/spec.md)

Rakitsu read both samples and the shared source. It stored the project procedure and a fresh generation session retrieved it. The empty-store run returned zero memories. Both saw the same image, brief, and project source, and both passed the same six first-pass checks. A single comparison therefore demonstrates procedure transfer and real component reuse. It leaves any improvement from the procedure itself unproven. The successful repair used specific browser feedback.

The samples both passed their Chrome smoke flows. During verification, an Observatory decorative overlay blocked the review button. The delivered fixture fixes that pointer handling. Both generation runs read the same pre-fix version; the shared kit stayed fixed across the comparison.
