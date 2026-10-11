---
title: Progress
type: schema
entity: progress
version: 1
schema:
  progress_of: Goal, the goal this update is about
  date: string, ISO date of the update
  summary: string, what was done, learned or decided
  next?(array): string, next steps
  blocker?(array): string, what stands in the way
settings:
  validation: warn
---

One update on a goal; a goal has many. Save in folder
progress/<goal folder name>/ with note_type progress, titled with the ISO
date followed by a few words on the update. Write a new progress
note for each update rather than editing an earlier one, and link it to its
goal with progress_of.
