Give THREE SEPARATE judgements. Do not let one bleed into another -- an item can be
highly relevant and not newsworthy (a well-known framework trending again), or newsworthy
and unimportant (an obscure lab ships an odd little model).

1. relevance 0-10 -- how squarely the SUBJECT sits in what this audience works on.
   9-10 core AI/ML work an AI engineer or researcher would act on
   6-8  clearly AI and clearly useful to them
   3-5  AI-adjacent, or AI applied in a domain they do not work in
   0-2  barely about AI

2. novelty 0-10 -- is there anything genuinely NEW here, as of the coverage day?
   9-10 first report of a material development: a model or product launched, weights or
        code released, a new result or capability demonstrated, an incident disclosed
   7-8  a real, specific release/result/finding that surfaced on the coverage day
   5-6  genuine but incremental: a point release, a small study, a routine paper
   3-4  mostly restatement: commentary, recap or roundup of something already reported,
        a project resurfacing with no new release behind it
   0-2  nothing new at all: an evergreen repo, tool, tutorial, docs page or course; an
        old project popular again; a "best X tools" list; marketing with no announcement
        in it; a repost
   Popularity is NOT novelty. Points, upvotes and stars-today measure attention, not
   news. A repository with thousands of stars today that has existed for two years and
   shipped nothing new is novelty 1, however many people are discussing it.

3. importance 0-10 -- how much it matters to the field if the claim holds, independently
   of relevance and novelty.
   9-10 major development (frontier model launch, breakthrough result, major open weights)
   7-8  significant (notable model/tool/paper, important infrastructure or safety work)
   4-6  solid but incremental (routine papers, minor releases, substantive discussion)
   1-3  marginal, promotional, or with no discernible contribution
   Judge the work, not the popularity of the source that carried it. A quiet paper with a
   real result outranks a heavily upvoted opinion thread.

4. include (true/false) -- should this appear in a daily AI NEWS digest at all?
   Set false when the item is not about AI/ML, OR when it is about AI but reports no news:
     - an existing project, repo, tool, model or dataset with no new release, version,
       result or incident behind it -- merely popular, trending again, or rediscovered
     - marketing, pricing pages, webinars, newsletters, "top N tools" listicles
     - tutorials, how-tos, beginner explainers, courses, documentation, awesome-lists
     - vague announcements with no substance: a teaser, a waitlist, a "coming soon",
       a partnership or rebrand with nothing technical in it
     - opinion, commentary, prediction and speculation that reports no new fact
     - recaps, roundups and "this week in AI" summaries of things already reported
     - job posts, event announcements, awards, and items too vague to identify
   As a rule: relevance <= 2, or novelty <= 2 with importance below 8, means false.

   Do NOT set false merely because an item is niche, academic, from a small or unknown
   lab, unpopular, a preprint (a paper released on the coverage day IS a new result), or
   incremental. Those get LOW SCORES, not exclusion. Breadth matters -- the ranking, not
   this flag, decides what makes the digest.

5. category -- EXACTLY ONE of:
   labs             frontier lab announcements, model launches, product/API releases
   research         papers and results not better covered by another category
   agents           agents, coding agents, tool use, computer use, MCP
   reasoning        reasoning, chain-of-thought, post-training, RL/RLHF/DPO, distillation
   open_source      open-weights models, open-source releases, notable repositories
   benchmarks       benchmarks, evaluations, leaderboards, measurement methodology
   infra            inference, serving, quantization, kernels, GPU/TPU, distributed training
   multimodal       vision, image/video/audio/speech, world models, robotics, embodied AI
   safety           alignment, safety, jailbreaks, prompt injection, misuse, governance
   interpretability mechanistic interpretability, probing, features, circuits
   Pick the single best fit -- the one a reader would look under. Always give a category,
   even when include is false.

6. story -- a 2-5 word lowercase-hyphenated slug naming the UNDERLYING story, so the same
   event reported by several sources gets the SAME slug, and so tomorrow's development in
   the same story can be given the same slug again. Name the story, not the article:
   "openai-gpt5-launch", not "openai-announces-gpt5-today". Two different papers must NOT
   share a slug.
