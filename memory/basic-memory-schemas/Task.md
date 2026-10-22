---
title: Task
type: schema
entity: task
version: 1
schema:
  task_of?: Goal, the goal this task serves, when one fits
  due?: string, ISO date or date-time when known
  amount?: string, the money involved with its currency, as stated
  with?(array): Person, who else is involved
settings:
  validation: warn
---

A concrete action the user has to take: an errand, a commitment they made,
a bill or invoice to pay. Link it with task_of to the goal it serves when
one fits; a task needs no goal. Save it with note_type task, titled with the
action, in folder tasks/<goal folder name>/ when it has a goal and in folder
tasks/ otherwise. A bill or invoice is a task titled Pay followed by the
payee and the invoice number when there is one, due when payment is due,
with its amount.

The note has no status. Track it with the items tools: save_item when it is
written or its due date moves, and close_item when it is done or cancelled;
a paid bill is done.
