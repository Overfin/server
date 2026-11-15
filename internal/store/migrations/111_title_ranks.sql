-- Each film's and series' rank on the server, by Overfin's value, among the
-- library's titles of its kind (1 the most valued): written again whenever
-- the values are (store.SetTitleRanks), read by title pages and the Rank
-- sort.
CREATE TABLE title_ranks (
  title_key TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  rank INTEGER NOT NULL
);
