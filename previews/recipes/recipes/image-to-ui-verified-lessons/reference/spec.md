# Sort Control UI/UX specification

Build a parcel sorting screen in the same workspace as Observatory and Rail Yard. Use the attached image for the desktop layout, visual hierarchy, grouping, and copy. The project kit may supply the exact visual treatments. Reuse suitable components from the available project source.

## Data and behavior

- Lane A1 has Jam detected, Lane B2 has Scanner offline, and Lane C4 is Running. Select Lane A1 on load. Metrics start at 1,240 parcels per hour, 7 / 8 active lanes, and 2 open alerts.
- Filter lanes by name, case-insensitively. Display an explicit empty state for no matches. Filtering away a selected lane preserves the detail and selection. Clearing the filter reveals the original selection.
- Choosing a row updates the detail and alert action. The running lane has no active alert action.
- The Review alert button opens a named modal for the active lane. Provide a note and explicit Save acknowledgement and Cancel actions. Escape and Cancel preserve state and return focus to Review alert.
- Save marks only that lane Acknowledged, reduces Open alerts once, and displays a readable confirmation. Keep the note available when changing lanes and returning within the session. The other lane retains its alert. Restore focus to the trigger or a logical successor if the trigger becomes unavailable.
- Enter or Space activates controls as appropriate. The modal contains keyboard focus. Use native controls or equivalent semantics and accessible names. Render hostile-looking note text as text.
- At 390px wide, stack the three sections in reading order without whole-page horizontal scrolling or hidden primary actions. Use an explicit label for the filter and note.

This test fixture uses synthetic records and browser session state. A production deployment requires the integration and review evidence in the certification checklist.
