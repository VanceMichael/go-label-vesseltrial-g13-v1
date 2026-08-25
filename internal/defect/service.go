package defect

import (
	"context"
	"strings"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	CreateDefect(context.Context, int64, model.Defect, string) (model.Defect, error)
	TransitionDefect(context.Context, int64, int64, int64, string, string, string) (model.Defect, error)
	ListDefects(context.Context, int64, string, string, int, int) ([]model.Defect, error)
}
type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }
func (s *Service) Open(ctx context.Context, actor model.User, input model.Defect, requestID string) (model.Defect, error) {
	if actor.Role != model.RoleShore && actor.Role != model.RoleCoordinator {
		return model.Defect{}, fault.New(fault.Forbidden, "role_forbidden", "shore operator or trial coordinator role is required")
	}
	input.Code, input.Title, input.Description = strings.TrimSpace(input.Code), strings.TrimSpace(input.Title), strings.TrimSpace(input.Description)
	if input.Code == "" || input.Title == "" || input.Description == "" || (input.Severity != "minor" && input.Severity != "major" && input.Severity != "critical") {
		return model.Defect{}, fault.New(fault.Invalid, "invalid_defect", "code, title, description and valid severity are required")
	}
	ctx = context.WithValue(ctx, "vesseltrial.defect-open", true)
	return s.repository.CreateDefect(ctx, actor.ID, input, requestID)
}
func (s *Service) Transition(ctx context.Context, actor model.User, id, version int64, target, note, requestID string) (model.Defect, error) {
	if actor.Role != model.RoleQuality {
		return model.Defect{}, fault.New(fault.Forbidden, "role_forbidden", "quality manager role is required")
	}
	if strings.TrimSpace(note) == "" {
		return model.Defect{}, fault.New(fault.Invalid, "action_note_required", "defect action note is required")
	}
	return s.repository.TransitionDefect(ctx, actor.ID, id, version, target, note, requestID)
}

func (s *Service) List(ctx context.Context, actor model.User, vesselID int64, status, severity string, limit, offset int) ([]model.Defect, error) {
	if actor.Role != model.RoleQuality && actor.Role != model.RoleCoordinator && actor.Role != model.RoleShore {
		return nil, fault.New(fault.Forbidden, "role_forbidden", "quality, trial or shore role is required")
	}
	return s.repository.ListDefects(ctx, vesselID, status, severity, limit, offset)
}
