General rules for calling the `jev` tool (TypeSafe AI's System One model),
shared across every example in this directory via `skills: [jev-usage]`.
The `criteria` shape rules below matter in particular — a `score` question
fed the wrong shape fails only at Jev's API layer, not at config load.

Pick the right question type by what the answer means, not by habit:
- `noul` — probability that a yes/no condition holds. `criteria` is an
  OPTIONAL object with `true`/`false` rubric text.
- `choice` — pick exactly one of a fixed set of named options. `criteria`
  is a REQUIRED object mapping each option name to its description.
- `score` — a continuous position along a described spectrum. `criteria`
  is a REQUIRED ORDERED ARRAY (2-10 elements) of level descriptions, each
  a plain string or an object like `{"what": "...", "examples": [...]}`.
  Levels are identified by their POSITION in the array, not by any field
  inside the element — never invent a numeric `score` field per level.
  **The returned value ranges from `0` to `len(criteria) - 1`, NOT a fixed
  0.0-1.0** — a 3-level criteria list returns 0 to 2.0, a 5-level list
  returns 0 to 4.0, and so on. If you need a 0.0-1.0 value (e.g. to apply
  a threshold written for that range), divide the returned score by
  `len(criteria) - 1` yourself — nothing does this for you. Confirmed
  live: a genuinely moderate finding scored ~0.98 against a 3-level
  criteria list, which silently clears a `>= 0.7` threshold meant only for
  the top level if you forget to normalize first.

Getting the `criteria` shape wrong for the type you used fails at Jev's
API layer with a 422, not at rakitsu's own config validation or
`rakitsu doctor` — get it right the first time rather than discovering it
via a live error.

Give Jev enough `state` to actually answer — the specific claim or finding
text, plus any grounding material relevant to it, not just a vague
restatement. If you have several independent judgments to make about the
same state, ask them as separate questions in one call rather than several
calls — they run in parallel and don't see each other's answers, so don't
rely on one question's phrasing referencing another's.

Never bundle more than one judgment into a single question (e.g. "is this
safe, with no security OR correctness concerns?") — split it into one
atomic question per concern instead. Confirmed live: the same real case
gave an opaque compound answer (0.03, correctly negative but silent on
*why*) versus two atomic questions on identical state giving
`security_concern=0.16` and `correctness_concern=0.97` — strictly more
actionable for the exact same facts, not just a style preference.

Don't expect a `noul` to hedge near 0.5 on a genuinely contested or
subjective claim — confirmed live, Jev commits to a side even on claims
reasonable people disagree about. And don't trust Jev to notice on its own
when the `state` you gave it doesn't actually address the question — a
mismatched reference (e.g. unrelated documentation) produced a noisier but
still opinion-shaped answer, not a clear "can't determine this." If your
example fetches or looks up evidence, your own prompt has to verify
relevance before trusting Jev's answer to it.

A `noul` near 0.5 means the model finds yes and no similarly likely, not
"medium intensity." A `choice`'s `confidence` and a `score`'s `confidence`
summarize how concentrated the probability distribution is, not whether
the underlying judgment is correct — don't treat high confidence as
permission to skip your own sanity-checking of what Jev actually said.
