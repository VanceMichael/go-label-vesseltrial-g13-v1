package model

import "time"

type Role string

const (
	RoleSurveyor    Role = "class_surveyor"
	RoleCoordinator Role = "trial_coordinator"
	RoleShore       Role = "shore_operator"
	RoleQuality     Role = "quality_manager"
)

type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Role         Role      `json:"role"`
	PasswordHash string    `json:"-"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"user_id"`
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type Vessel struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	CCSNumber       string    `json:"ccs_number"`
	Owner           string    `json:"owner"`
	BatteryCapacity int       `json:"battery_capacity_kwh"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
}

type ClassReview struct {
	ID          int64     `json:"id"`
	VesselID    int64     `json:"vessel_id"`
	Status      string    `json:"status"`
	SubmittedBy int64     `json:"submitted_by"`
	Version     int64     `json:"version"`
	Notes       string    `json:"notes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ReviewDecision struct {
	ID         int64     `json:"id"`
	ReviewID   int64     `json:"review_id"`
	ReviewerID int64     `json:"reviewer_id"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
}

type TrialPlan struct {
	ID        int64     `json:"id"`
	VesselID  int64     `json:"vessel_id"`
	ReviewID  int64     `json:"review_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Timezone  string    `json:"timezone"`
	Version   int64     `json:"version"`
	CreatedBy int64     `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type VoyageLeg struct {
	ID         int64     `json:"id"`
	PlanID     int64     `json:"plan_id"`
	VesselID   int64     `json:"vessel_id"`
	Name       string    `json:"name"`
	Channel    string    `json:"channel"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	Status     string    `json:"status"`
	Version    int64     `json:"version"`
	ReleasedBy *int64    `json:"released_by,omitempty"`
}

type ShoreStation struct {
	ID         int64      `json:"id"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Status     string     `json:"status"`
}

type TelemetrySample struct {
	ID         int64     `json:"id"`
	VesselID   int64     `json:"vessel_id"`
	LegID      *int64    `json:"leg_id,omitempty"`
	StationID  int64     `json:"station_id"`
	Sequence   int64     `json:"sequence"`
	BatteryPct float64   `json:"battery_pct"`
	SpeedKnots float64   `json:"speed_knots"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	ObservedAt time.Time `json:"observed_at"`
	ReceivedAt time.Time `json:"received_at"`
}

type Defect struct {
	ID          int64     `json:"id"`
	VesselID    int64     `json:"vessel_id"`
	LegID       *int64    `json:"leg_id,omitempty"`
	Code        string    `json:"code"`
	Severity    string    `json:"severity"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Version     int64     `json:"version"`
	CreatedBy   int64     `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type DefectAction struct {
	ID        int64     `json:"id"`
	DefectID  int64     `json:"defect_id"`
	ActorID   int64     `json:"actor_id"`
	Action    string    `json:"action"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type DeliveryBatch struct {
	ID         int64      `json:"id"`
	VesselID   int64      `json:"vessel_id"`
	BatchNo    string     `json:"batch_no"`
	Status     string     `json:"status"`
	Version    int64      `json:"version"`
	ReleasedBy *int64     `json:"released_by,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type AuditEvent struct {
	ID         int64     `json:"id"`
	ActorID    int64     `json:"actor_id"`
	ObjectType string    `json:"object_type"`
	ObjectID   int64     `json:"object_id"`
	Action     string    `json:"action"`
	Outcome    string    `json:"outcome"`
	RequestID  string    `json:"request_id"`
	Details    string    `json:"details"`
	CreatedAt  time.Time `json:"created_at"`
}

type WorkerJob struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Payload     string     `json:"payload"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	AvailableAt time.Time  `json:"available_at"`
	LockedUntil *time.Time `json:"locked_until,omitempty"`
	ClaimToken  string     `json:"-"`
	LastError   string     `json:"last_error"`
}
