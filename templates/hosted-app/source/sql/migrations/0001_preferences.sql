CREATE TABLE preferences (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL CHECK (version = 1),
  selected_tab TEXT NOT NULL CHECK (selected_tab IN ('overview', 'preferences')),
  period TEXT NOT NULL CHECK (period IN ('1h', '24h', '7d'))
) STRICT;
