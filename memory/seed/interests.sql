-- The curated news interests (owner ''), seen by every user: name, whether
-- every user follows it (1), its rubric and its RSS/Atom feeds. Each rubric
-- is prose in rubrics/*.md. Run by `task init` with the sqlite3 CLI (for
-- readfile) from the repo root; safe to repeat. Interests are added or
-- updated, and curated ones no longer listed here are deleted with their
-- follows.
PRAGMA foreign_keys = ON;
BEGIN;
CREATE TEMP TABLE seed (interest TEXT NOT NULL, is_default INTEGER NOT NULL, rubric TEXT NOT NULL, sources TEXT NOT NULL);
INSERT INTO seed (interest, is_default, rubric, sources) VALUES
  ('Positive living', 1, readfile('memory/seed/rubrics/positive-living.md'), json_array(
        'https://greatergood.berkeley.edu/site/rss/articles',
        'https://www.positive.news/feed/',
        'https://www.goodnewsnetwork.org/feed/')),
  ('Stoic principles', 1, readfile('memory/seed/rubrics/stoic-principles.md'), json_array(
        'https://dailystoic.com/feed/',
        'https://modernstoicism.com/feed/',
        'https://donaldrobertson.substack.com/feed')),
  ('Healthy eating', 1, readfile('memory/seed/rubrics/healthy-eating.md'), json_array(
        'https://www.hsph.harvard.edu/nutritionsource/feed/',
        'https://www.sciencedaily.com/rss/health_medicine/nutrition.xml',
        'https://nutritionfacts.org/feed/')),
  ('Science', 1, readfile('memory/seed/rubrics/science.md'), json_array(
        'https://www.sciencedaily.com/rss/top/science.xml',
        'https://www.quantamagazine.org/feed/',
        'https://phys.org/rss-feed/',
        'https://www.nature.com/nature.rss')),
  ('Positive scientific developments across the world', 1, readfile('memory/seed/rubrics/positive-scientific-developments.md'), json_array(
        'https://fixthenews.com/feed',
        'https://www.goodnewsnetwork.org/category/news/science/feed/',
        'https://ourworldindata.org/atom.xml')),
  ('Neuroscience for better living', 1, readfile('memory/seed/rubrics/neuroscience-for-better-living.md'), json_array(
        'https://www.sciencedaily.com/rss/mind_brain.xml',
        'https://www.psypost.org/feed/',
        'https://www.thetransmitter.org/feed/',
        'https://greatergood.berkeley.edu/site/rss/articles')),
  ('Statistics', 0, readfile('memory/seed/rubrics/statistics.md'), json_array(
        'https://statmodeling.stat.columbia.edu/feed/',
        'https://rss.arxiv.org/rss/stat.ME',
        'https://rss.arxiv.org/rss/stat.ML')),
  ('Machine learning research', 0, readfile('memory/seed/rubrics/machine-learning-research.md'), json_array(
        'https://research.google/blog/rss/',
        'https://bair.berkeley.edu/blog/feed.xml',
        'https://rss.arxiv.org/rss/cs.LG')),
  ('Neural networks and computational neuroscience', 0, readfile('memory/seed/rubrics/neural-networks-computational-neuroscience.md'), json_array(
        'https://rss.arxiv.org/rss/cs.NE',
        'https://rss.arxiv.org/rss/q-bio.NC')),
  ('AI & Agentic Development', 0, readfile('memory/seed/rubrics/ai-agentic-development.md'), json_array(
        'https://openai.com/news/rss.xml',
        'https://deepmind.google/blog/rss.xml',
        'https://mistral.ai/rss.xml',
        'https://huggingface.co/blog/feed.xml',
        'https://blogs.nvidia.com/feed/',
        'https://www.apple.com/newsroom/rss-feed.rss',
        'https://www.latent.space/feed',
        'https://sifted.eu/feed',
        'https://nlp.elvissaravia.com/feed',
        'https://arxiviq.substack.com/feed',
        'https://aiweekly.co/issues.rss',
        'https://news.ycombinator.com/rss',
        'https://simonwillison.net/atom/everything/',
        'https://rss.arxiv.org/rss/cs.MA'));
INSERT INTO interests (interest, owner, sources, rubric, is_default, created_at, updated_at)
SELECT interest, '', sources, CAST(rubric AS TEXT), is_default, unixepoch(), unixepoch() FROM seed WHERE true
ON CONFLICT (owner, interest) DO UPDATE SET
    interest = excluded.interest, sources = excluded.sources, rubric = excluded.rubric,
    is_default = excluded.is_default, updated_at = excluded.updated_at;
DELETE FROM interests WHERE owner = '' AND interest NOT IN (SELECT interest FROM seed);
DROP TABLE seed;
COMMIT;
