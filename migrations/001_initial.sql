CREATE TABLE users (
 id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('class_surveyor','trial_coordinator','shore_operator','quality_manager')),
 password_hash TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)), created_at TEXT NOT NULL
);
CREATE TABLE sessions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id), token_hash TEXT NOT NULL UNIQUE,
 expires_at TEXT NOT NULL, revoked_at TEXT, created_at TEXT NOT NULL
);
CREATE INDEX sessions_user_active_idx ON sessions(user_id, expires_at, revoked_at);
CREATE TABLE vessels (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, ccs_number TEXT NOT NULL UNIQUE, owner TEXT NOT NULL,
 battery_capacity_kwh INTEGER NOT NULL CHECK(battery_capacity_kwh > 0), version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL
);
CREATE TABLE class_reviews (
 id INTEGER PRIMARY KEY AUTOINCREMENT, vessel_id INTEGER NOT NULL REFERENCES vessels(id),
 status TEXT NOT NULL CHECK(status IN ('draft','submitted','approved','rejected')), submitted_by INTEGER NOT NULL REFERENCES users(id),
 version INTEGER NOT NULL DEFAULT 1, notes TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX class_reviews_vessel_status_idx ON class_reviews(vessel_id, status);
CREATE TABLE review_decisions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, review_id INTEGER NOT NULL REFERENCES class_reviews(id), reviewer_id INTEGER NOT NULL REFERENCES users(id),
 decision TEXT NOT NULL CHECK(decision IN ('approved','rejected')), reason TEXT NOT NULL, created_at TEXT NOT NULL,
 UNIQUE(review_id, reviewer_id, decision)
);
CREATE TABLE trial_plans (
 id INTEGER PRIMARY KEY AUTOINCREMENT, vessel_id INTEGER NOT NULL REFERENCES vessels(id), review_id INTEGER NOT NULL REFERENCES class_reviews(id),
 name TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('draft','scheduled','active','completed','cancelled')), timezone TEXT NOT NULL,
 version INTEGER NOT NULL DEFAULT 1, created_by INTEGER NOT NULL REFERENCES users(id), created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(vessel_id, name)
);
CREATE TABLE voyage_legs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, plan_id INTEGER NOT NULL REFERENCES trial_plans(id), vessel_id INTEGER NOT NULL REFERENCES vessels(id),
 name TEXT NOT NULL, channel TEXT NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('planned','released','underway','completed','held')), version INTEGER NOT NULL DEFAULT 1,
 released_by INTEGER REFERENCES users(id), CHECK(starts_at < ends_at), UNIQUE(plan_id, name)
);
CREATE INDEX voyage_legs_window_idx ON voyage_legs(vessel_id, starts_at, ends_at, status);
CREATE TABLE shore_stations (
 id INTEGER PRIMARY KEY AUTOINCREMENT, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, last_seen_at TEXT,
 status TEXT NOT NULL CHECK(status IN ('online','stale','offline'))
);
CREATE TABLE telemetry_samples (
 id INTEGER PRIMARY KEY AUTOINCREMENT, vessel_id INTEGER NOT NULL REFERENCES vessels(id), leg_id INTEGER REFERENCES voyage_legs(id),
 station_id INTEGER NOT NULL REFERENCES shore_stations(id), sequence INTEGER NOT NULL,
 battery_pct REAL NOT NULL CHECK(battery_pct BETWEEN 0 AND 100), speed_knots REAL NOT NULL CHECK(speed_knots >= 0),
 latitude REAL NOT NULL CHECK(latitude BETWEEN -90 AND 90), longitude REAL NOT NULL CHECK(longitude BETWEEN -180 AND 180),
 observed_at TEXT NOT NULL, received_at TEXT NOT NULL, UNIQUE(vessel_id, station_id, sequence)
);
CREATE INDEX telemetry_vessel_time_idx ON telemetry_samples(vessel_id, observed_at DESC);
CREATE TABLE defects (
 id INTEGER PRIMARY KEY AUTOINCREMENT, vessel_id INTEGER NOT NULL REFERENCES vessels(id), leg_id INTEGER REFERENCES voyage_legs(id),
 code TEXT NOT NULL, severity TEXT NOT NULL CHECK(severity IN ('minor','major','critical')), title TEXT NOT NULL, description TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('open','contained','verified','closed')), version INTEGER NOT NULL DEFAULT 1,
 created_by INTEGER NOT NULL REFERENCES users(id), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(vessel_id, code)
);
CREATE INDEX defects_gate_idx ON defects(vessel_id, status, severity);
CREATE TABLE defect_actions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, defect_id INTEGER NOT NULL REFERENCES defects(id), actor_id INTEGER NOT NULL REFERENCES users(id),
 action TEXT NOT NULL, note TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE delivery_batches (
 id INTEGER PRIMARY KEY AUTOINCREMENT, vessel_id INTEGER NOT NULL REFERENCES vessels(id), batch_no TEXT NOT NULL UNIQUE,
 status TEXT NOT NULL CHECK(status IN ('preparing','quality_gate','released','delivered')), version INTEGER NOT NULL DEFAULT 1,
 released_by INTEGER REFERENCES users(id), released_at TEXT, created_at TEXT NOT NULL
);
CREATE TABLE worker_jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, payload TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','running','succeeded','retry','failed')), attempts INTEGER NOT NULL DEFAULT 0,
 max_attempts INTEGER NOT NULL, available_at TEXT NOT NULL, locked_until TEXT, last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX worker_jobs_claim_idx ON worker_jobs(status, available_at, locked_until);
CREATE TABLE idempotency_keys (
 id INTEGER PRIMARY KEY AUTOINCREMENT, actor_id INTEGER NOT NULL REFERENCES users(id), method TEXT NOT NULL, route TEXT NOT NULL,
 idem_key TEXT NOT NULL, response_code INTEGER NOT NULL, resource_type TEXT NOT NULL, resource_id INTEGER NOT NULL,
 created_at TEXT NOT NULL, expires_at TEXT NOT NULL, UNIQUE(actor_id, method, route, idem_key)
);
CREATE TABLE audit_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, actor_id INTEGER NOT NULL REFERENCES users(id), object_type TEXT NOT NULL,
 object_id INTEGER NOT NULL, action TEXT NOT NULL, outcome TEXT NOT NULL, request_id TEXT NOT NULL, details TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX audit_object_idx ON audit_events(object_type, object_id, created_at);
