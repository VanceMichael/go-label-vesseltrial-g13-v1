package trial

import (
	"context"
	"strings"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	CreatePlan(context.Context, int64, int64, int64, string, string, string) (model.TrialPlan, error)
	AddLeg(context.Context, int64, int64, model.VoyageLeg, string) (model.VoyageLeg, error)
	SchedulePlan(context.Context, int64, int64, int64, string) (model.TrialPlan, error)
	ReleaseLeg(context.Context, int64, int64, int64, string) (model.VoyageLeg, error)
	HasActiveWindow(context.Context, int64) (bool, error)
	ReleaseLegUnchecked(context.Context, int64, int64, int64, string) (model.VoyageLeg, error)
	CompleteLeg(context.Context, int64, int64, int64, string) (model.VoyageLeg, error)
	ListLegs(context.Context, int64, string, int, int) ([]model.VoyageLeg, error)
}

type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func authorize(actor model.User) error {
	if actor.Role != model.RoleCoordinator {
		return fault.New(fault.Forbidden, "role_forbidden", "trial coordinator role is required")
	}
	return nil
}

func (s *Service) CreatePlan(ctx context.Context, actor model.User, vesselID, reviewID int64, name, timezone, requestID string) (model.TrialPlan, error) {
	if err := authorize(actor); err != nil {
		return model.TrialPlan{}, err
	}
	name, timezone = strings.TrimSpace(name), strings.TrimSpace(timezone)
	if name == "" || timezone == "" {
		return model.TrialPlan{}, fault.New(fault.Invalid, "invalid_plan", "plan name and timezone are required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return model.TrialPlan{}, fault.New(fault.Invalid, "invalid_timezone", "timezone must be an IANA location")
	}
	return s.repository.CreatePlan(ctx, actor.ID, vesselID, reviewID, name, timezone, requestID)
}

func (s *Service) AddLeg(ctx context.Context, actor model.User, planID int64, leg model.VoyageLeg, requestID string) (model.VoyageLeg, error) {
	if err := authorize(actor); err != nil {
		return model.VoyageLeg{}, err
	}
	leg.Name, leg.Channel = strings.TrimSpace(leg.Name), strings.TrimSpace(leg.Channel)
	if leg.Name == "" || leg.Channel == "" || !leg.StartsAt.Before(leg.EndsAt) {
		return model.VoyageLeg{}, fault.New(fault.Invalid, "invalid_leg", "leg name, channel and valid time window are required")
	}
	return s.repository.AddLeg(ctx, actor.ID, planID, leg, requestID)
}

func (s *Service) Schedule(ctx context.Context, actor model.User, planID, version int64, requestID string) (model.TrialPlan, error) {
	if err := authorize(actor); err != nil {
		return model.TrialPlan{}, err
	}
	return s.repository.SchedulePlan(ctx, actor.ID, planID, version, requestID)
}

func (s *Service) Release(ctx context.Context, actor model.User, legID, version int64, requestID string) (model.VoyageLeg, error) {
	return s.ReleaseWithWindowPrecheck(ctx, actor, legID, version, requestID)
}

func (s *Service) ReleaseWithWindowPrecheck(ctx context.Context, actor model.User, legID, version int64, requestID string) (model.VoyageLeg, error) {
	if err := authorize(actor); err != nil {
		return model.VoyageLeg{}, err
	}
	overlaps, err := s.repository.HasActiveWindow(ctx, legID)
	if err != nil {
		return model.VoyageLeg{}, err
	}
	if overlaps {
		return model.VoyageLeg{}, fault.New(fault.Conflict, "active_window_conflict", "another active leg overlaps this vessel window")
	}
	return s.repository.ReleaseLegUnchecked(ctx, actor.ID, legID, version, requestID)
}

func (s *Service) Complete(ctx context.Context, actor model.User, legID, version int64, requestID string) (model.VoyageLeg, error) {
	if err := authorize(actor); err != nil {
		return model.VoyageLeg{}, err
	}
	return s.repository.CompleteLeg(ctx, actor.ID, legID, version, requestID)
}

func (s *Service) ListLegs(ctx context.Context, actor model.User, vesselID int64, status string, limit, offset int) ([]model.VoyageLeg, error) {
	if actor.Role != model.RoleCoordinator && actor.Role != model.RoleSurveyor && actor.Role != model.RoleQuality {
		return nil, fault.New(fault.Forbidden, "role_forbidden", "trial, survey or quality role is required")
	}
	return s.repository.ListLegs(ctx, vesselID, status, limit, offset)
}
