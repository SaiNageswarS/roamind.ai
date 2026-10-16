---
title: Exercise
type: schema
entity: exercise
version: 1
schema:
  exercise_of: Goal, the learning goal the exercise trains
  date: string, ISO date the exercise was posted
  topic: string, the skill or concept it practises
  difficulty(enum): [intro, easy, medium, hard, stretch]
  format: string, the kind of exercise and how many items it has
  result?(enum): [correct, partial, incorrect]
  score?: string, items right out of items asked, written right/total
  feedback?: string, the mistakes made and what they show
  next?(array): string, what the next exercises should practise
settings:
  validation: warn
---

One daily exercise for a learning goal, in folder exercises/<goal folder
name>/ with note_type exercise, titled with the ISO date followed by the
topic and linked to its goal with exercise_of. It is saved when the exercise
is posted, with the full exercise under the heading Exercise, however long.
When the user answers, append their answer under the heading Answer and add
result, score, feedback and next. The latest exercises set the topic, format
and difficulty of the next one.
