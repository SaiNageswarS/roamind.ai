---
title: Preference
type: schema
entity: preference
version: 1
schema:
  preference: string, the preference itself, as one self-contained statement
  subject(enum, topic; also the folder under preferences/): [food, travel, fitness, health, work, tech, communication, shopping, entertainment, home, finance, other]
  evidence(enum, how it was learned; channel and date go in its context): [stated, inferred, observed, corrected]
settings:
  validation: warn
---

One preference per note: how the user likes one thing done. Save in folder
preferences/<subject>/ with note_type preference, titled with the
preference itself. The note has exactly three observations, with the
categories preference, subject and evidence. The evidence value is how the
preference was learned, followed by the channel and ISO date in parentheses. How it was learned is one of stated (the user said it
directly), inferred (deduced from what the user said), observed (a pattern
in what the user does or asks) or corrected (the user corrected an earlier
preference).

When a preference changes, update its note, evidence included, rather than
adding a second one.
