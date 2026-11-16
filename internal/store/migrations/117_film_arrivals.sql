-- When each film arrived, looked up by its TMDB ID: "latest" (the site's
-- Recently added) orders films by their arrival, not by their copy's
-- date, which an upgrade changes.
CREATE INDEX title_history_films ON title_history (tmdb, at)
    WHERE kind = 'movie' AND event = 'arrived';
