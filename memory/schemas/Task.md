---
title: Task
type: schema
entity: task
version: 1
schema:
  task_of: Goal, the goal this task serves
  status(enum): [todo, doing, done, cancelled]
  due?: string, ISO date or date-time when known
  with?(array): Person, who else is involved
settings:
  validation: warn
---

A concrete action towards a goal. Every task belongs to exactly one goal:
link it with task_of to the goal it serves, and when no existing goal fits,
ask the user which goal it belongs to or offer to create one. Save in folder
tasks/<goal folder name>/ with note_type task, titled with the action, e.g.
Book parents' travel. Update its status as it moves along.
