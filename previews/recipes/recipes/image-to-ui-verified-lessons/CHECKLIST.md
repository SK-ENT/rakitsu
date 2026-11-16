# UI certification checklist

Use one row per requirement in the result. Record Pass, Fail, or Unverified and link the evidence. A screen is certified for its specified scope only when every required row passes. A single failure or unverified required row blocks certification. Product and UX owners approve the final result against the target specification.

| ID | Required evidence |
| --- | --- |
| VIS-01 | Desktop screenshot compared to the image. Verify hierarchy, region order, copy, spacing, palette, and density. Review and approve any differences. |
| REU-01 | Source imports the shared kit. Queue, metric, and drawer are actual registered kit instances in the browser. Their core behaviors run through the kit contracts. |
| UX-01 | Initial state and labels match the spec, including the selected row and all three metrics. |
| UX-02 | Filter, search miss, clear, selection retention, details, and running lane all behave as specified. |
| UX-03 | Review opens the correct alert. Save is scoped and idempotent. Confirmation, count, and note survive changing lanes within the stated session. Cancel and Escape preserve state. |
| A11Y-01 | Controls have names and visible focus. Keyboard users can complete the flow. Dialog traps focus and restores it to the trigger or a logical successor. |
| A11Y-02 | Check contrast, zoom, screen-reader announcements, and reduced motion. |
| RESP-01 | At desktop and 390px, required content and actions stay visible and usable. Verify reading order and no page overflow. |
| ROB-01 | No page errors, missing assets, or unhandled failures in the tested flows. Notes are rendered as text, including hostile-looking strings. |
| PROD-01 | Verify the production API and persistence, permissions, loading/empty/error states, and slow or duplicate submissions against the actual integration. |
| PROD-02 | Check target browser and device support, performance budget, monitoring, and product/UX acceptance. |

Record memory write, fresh-session retrieval, and the empty-store baseline separately. They tell us whether the Rakitsu harness contributed. The screen's certification depends on the requirements above.
