# Image to UI with reusable components and verified lessons

Tested source revision: a3633661 (dev build, modified worktree). The
minimum compatible release is unverified. Use an internal Rakitsu build
with the Codex provider, image attachment, native project memory and
procedure auto-recall. The original runs used `gpt-6.1-sol` with an existing
Codex sign-in. Set `UI_LAB_MODEL` and `UI_LAB_RAKITSU` if needed.

## Run the loop

Run from a fresh extraction on a trusted local machine. Install Python 3,
Node.js, Chrome and `puppeteer-core` (`npm install --no-save puppeteer-core`).
Set `UI_LAB_CHROME` to the Chrome executable if required. The reference image
and written spec are sent to your configured Codex provider. The YAML
grants read access to specific project files and uses project-scoped memory.

1. Learn the kit from the two starter apps, then generate Sort Control twice.

   ```sh
   "${UI_LAB_RAKITSU:-rakitsu}" run harness/learn.yaml \
     'Inspect apps/observatory/index.html, apps/rail-yard/index.html, kit/components.js and kit/tokens.css. Save the prefab procedure.' \
     --model "${UI_LAB_MODEL:-gpt-6.1-sol}" --no-hub --timeout 120 --sessions-dir harness/state/learner
   bash harness/run-arm.sh baseline
   bash harness/run-arm.sh recall
   node harness/check.mjs generated/baseline generated/recall
   ```

2. If a check fails in the recall arm, give the measured failure and
   expected behavior to the repair config. Review generated code before
   running it. In the recorded trial, the failure was modal keyboard focus.

   ```sh
   "${UI_LAB_RAKITSU:-rakitsu}" run harness/repair.yaml \
     'Inspect generated/recall/index.html and kit/components.js. Fix modal Tab and Shift+Tab wrapping and restore trigger focus on Escape.' \
     --model "${UI_LAB_MODEL:-gpt-6.1-sol}" --no-hub --timeout 120 --sessions-dir harness/state/repair \
     > harness/repair.log 2>&1
   python3 harness/materialize.py repair
   node harness/check.mjs generated/recall generated/repaired
   ```

3. After measuring a failure and verifying its repair, fork the prefab
   memory into separate stores. Save the keyboard lesson only in the
   learned arm, then run the held-out Intake Watch task in fresh sessions.

   ```sh
   mkdir -p followup/state
   cp -R harness/state/memory followup/state/control-memory
   cp -R harness/state/memory followup/state/learned-memory
   "${UI_LAB_RAKITSU:-rakitsu}" run followup/learn.yaml \
     'Read evidence/browser-checks.json, generated/recall/index.html, generated/repaired/index.html and kit/components.js. Save the verified keyboard lesson.' \
     --model "${UI_LAB_MODEL:-gpt-6.1-sol}" --no-hub --timeout 120 --sessions-dir followup/state/learner
   bash followup/run-arm.sh control
   bash followup/run-arm.sh learned
   node harness/check.mjs --intake
   ```

If your first output already passes, record that result and skip the repair
and lesson steps until you have a measured failure and successful repair.
Run outputs and logs are created locally by the commands above.

See [checklist](CHECKLIST.md), [Sort Control evidence notes](CERTIFICATION.md)
and [Intake Watch results](followup/RESULTS.md).
