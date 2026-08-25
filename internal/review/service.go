package review

import (
	"context"
	"strings"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	CreateVessel(context.Context, model.Vessel) (model.Vessel, error)
	CreateReview(context.Context, int64, int64, string, string) (model.ClassReview, error)
	SubmitReview(context.Context, int64, int64, int64, string, string) (model.ClassReview, error)
	DecideReview(context.Context, int64, int64, int64, string, string, string) (model.ClassReview, error)
	Review(context.Context, int64) (model.ClassReview, error)
}

type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) RegisterVessel(ctx context.Context, actor model.User, vessel model.Vessel) (model.Vessel, error) {
	if actor.Role != model.RoleCoordinator {
		return model.Vessel{}, fault.New(fault.Forbidden, "role_forbidden", "trial coordinator role is required")
	}
	vessel.Name, vessel.CCSNumber, vessel.Owner = strings.TrimSpace(vessel.Name), strings.TrimSpace(vessel.CCSNumber), strings.TrimSpace(vessel.Owner)
	if vessel.Name == "" || vessel.CCSNumber == "" || vessel.Owner == "" || vessel.BatteryCapacity <= 0 {
		return model.Vessel{}, fault.New(fault.Invalid, "invalid_vessel", "name, CCS number, owner and battery capacity are required")
	}
	return s.repository.CreateVessel(ctx, vessel)
}

func (s *Service) Draft(ctx context.Context, actor model.User, vesselID int64, notes, requestID string) (model.ClassReview, error) {
	if actor.Role != model.RoleCoordinator {
		return model.ClassReview{}, fault.New(fault.Forbidden, "role_forbidden", "trial coordinator role is required")
	}
	if strings.TrimSpace(notes) == "" {
		return model.ClassReview{}, fault.New(fault.Invalid, "review_notes_required", "review notes are required")
	}
	return s.repository.CreateReview(ctx, actor.ID, vesselID, notes, requestID)
}

func (s *Service) Submit(ctx context.Context, actor model.User, reviewID, version int64, idemKey, requestID string) (model.ClassReview, error) {
	if actor.Role != model.RoleCoordinator {
		return model.ClassReview{}, fault.New(fault.Forbidden, "role_forbidden", "trial coordinator role is required")
	}
	return s.repository.SubmitReview(ctx, actor.ID, reviewID, version, idemKey, requestID)
}

func (s *Service) Decide(ctx context.Context, actor model.User, reviewID, version int64, decision, reason, requestID string) (model.ClassReview, error) {
	if actor.Role != model.RoleSurveyor {
		return model.ClassReview{}, fault.New(fault.Forbidden, "role_forbidden", "class surveyor role is required")
	}
	if strings.TrimSpace(reason) == "" {
		return model.ClassReview{}, fault.New(fault.Invalid, "decision_reason_required", "decision reason is required")
	}
	return s.repository.DecideReview(ctx, actor.ID, reviewID, version, decision, reason, requestID)
}
