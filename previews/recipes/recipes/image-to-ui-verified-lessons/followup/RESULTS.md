# Intake Watch first-pass certification trial

Rakitsu wrote a project-scoped procedure called `modal-keyboard-certification` after reading the Sort Control browser failure, first output, repaired output, and shared drawer code. It preserved the original prefab procedure.

## What was held constant

Two fresh `gpt-6.1-sol` sessions saw the same [new reference image](reference/intake.png), [spec](reference/spec.md), and sample source and kit. The control retained the original prefab procedure. The learned arm also had the new keyboard procedure. Both retrieved their expected procedures. The earlier generated screens were outside both generators' source allowlist. The saved HTML on disk matches each first complete agent output.

Intake Watch changes the domain, palette and desktop panel order, and adds a priority select alongside the note. Its spec explicitly requires both keyboard tab directions to wrap in the modal.

| Automated check | Prefab only | Prefab + keyboard lesson |
| --- | --- | --- |
| REU-01 Shared kit | Pass | Pass |
| UX-01 Initial state | Pass | Pass |
| UX-02 Filtering and selection | Pass | Pass |
| UX-03 Save, Cancel and triage state | Pass | Pass |
| A11Y-01 Modal keyboard cycle and Escape focus return | Pass | Pass |
| ROB-01 Safe note rendering and page errors | Pass | Pass |
| RESP-01 Desktop layout and 390px reading order | Pass | Pass |

Both candidates preserved the desktop hierarchy and dark palette in a visual spot check. Visual owner approval (VIS-01), full accessibility (A11Y-02), production integration (PROD-01), and release approval (PROD-02) remain unverified per the [checklist](../CHECKLIST.md).

The learned arm retrieved the lesson and implemented its open-dialog Tab cycling pattern. The control implemented its own boundary wrapping from the written requirement and also passed. This test demonstrates reuse of verified know-how on a fresh task. It provides no observed first-pass checklist advantage from that lesson on this case.


Run both first outputs through the same browser checks using `node harness/check.mjs --intake` from the lab root.
