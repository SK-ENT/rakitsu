# Intake Watch UI/UX specification

Use the attached desktop image as the visual reference for a new Harbor Utilities screen. Use the existing shared queue, metric cards and drawer when the contracts fit. The desktop detail panel sits left of the queue. On narrow screens show metrics, queue, then detail. Use the screenshot's headings, label text, and initial values.

- Pump M1 starts selected with Pressure drift and “Last reading 21:06 / East basin intake”. Pump N2 has Intake blocked, and Pump P4 is Nominal. Flow rate is 860 L/s, Pumps online is 5 / 6, and Open flags is 2.
- Filter pumps by name, case-insensitively. Explicitly show an empty state. Filtering away the selected row preserves details and selection. Clearing reveals the original selection. Choosing a row updates its detail. A nominal pump has no active Review flag action.
- Review flag opens a named modal for that pump. Give it a labeled Triage priority select (Monitor, Maintenance, Urgent), a labeled Note field, Cancel and Save triage actions. Default to Monitor. Keyboard users can open and operate it, and Tab and Shift+Tab cycle inside it without leaving. Escape and Cancel discard edits, preserve the flag count and restore trigger focus.
- Save marks only that pump Triaged, reduces Open flags only once, shows a confirmation, and preserves the priority and note in the visible details when returning to that pump during the same session. A different flag remains unchanged. Restore focus to the trigger or a logical successor when the trigger is removed. Render arbitrary note text safely as text.
- Use semantic controls, explicit input labels and visible keyboard focus. At 390px the screen keeps its content and actions accessible without page-level horizontal scrolling.
- This is a synthetic browser fixture. Live services and durable storage are outside this check and need their own production evidence.
