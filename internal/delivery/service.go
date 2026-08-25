package delivery

import (
	"context"
	"strings"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	CreateDeliveryBatch(context.Context, int64, int64, string, string) (model.DeliveryBatch, error)
	EvaluateDelivery(context.Context, int64, int64, int64, bool, string) (model.DeliveryBatch, error)
}
type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }
func authorize(actor model.User) error {
	if actor.Role != model.RoleQuality {
		return fault.New(fault.Forbidden, "role_forbidden", "quality manager role is required")
	}
	return nil
}
func (s *Service) Create(ctx context.Context, actor model.User, vesselID int64, batchNo, requestID string) (model.DeliveryBatch, error) {
	if err := authorize(actor); err != nil {
		return model.DeliveryBatch{}, err
	}
	batchNo = strings.TrimSpace(batchNo)
	if batchNo == "" {
		return model.DeliveryBatch{}, fault.New(fault.Invalid, "batch_number_required", "delivery batch number is required")
	}
	return s.repository.CreateDeliveryBatch(ctx, actor.ID, vesselID, batchNo, requestID)
}
func (s *Service) Gate(ctx context.Context, actor model.User, batchID, version int64, release bool, requestID string) (model.DeliveryBatch, error) {
	if err := authorize(actor); err != nil {
		return model.DeliveryBatch{}, err
	}
	return s.repository.EvaluateDelivery(ctx, actor.ID, batchID, version, release, requestID)
}
