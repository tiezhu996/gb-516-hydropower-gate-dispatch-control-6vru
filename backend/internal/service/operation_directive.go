package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/constants"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/repository"
)

type OperationDirectiveService interface {
	List(context.Context, dto.PageQuery) (repository.Page[model.OperationDirective], error)
	Get(context.Context, uint) (model.OperationDirective, error)
	Create(context.Context, dto.CreateOperationDirective, string, string) (model.OperationDirective, error)
	Update(context.Context, uint, dto.UpdateOperationDirective, string, string) (model.OperationDirective, error)
	Transition(context.Context, uint, dto.TransitionRequest, string, string, string) (model.OperationDirective, error)
	Delete(context.Context, uint, string, string) error
	StatusCounts(context.Context) (map[string]int64, error)
}

type operationDirectiveService struct {
	repository repository.OperationDirectiveRepository
	gates      repository.GateUnitRepository
	reservoirs repository.ReservoirRepository
	security   SecurityService
}

func NewOperationDirectiveService(repo repository.OperationDirectiveRepository, gates repository.GateUnitRepository, reservoirs repository.ReservoirRepository, security SecurityService) OperationDirectiveService {
	return &operationDirectiveService{repository: repo, gates: gates, reservoirs: reservoirs, security: security}
}

func (s *operationDirectiveService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.OperationDirective], error) {
	return s.repository.List(ctx, query)
}

func (s *operationDirectiveService) Get(ctx context.Context, id uint) (model.OperationDirective, error) {
	return s.repository.Get(ctx, id)
}

func (s *operationDirectiveService) Create(ctx context.Context, input dto.CreateOperationDirective, actor, requestID string) (model.OperationDirective, error) {
	if err := validateOperationDirectiveBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.OperationDirective{}, err
	}
	if err := validatePermitWindow(input.PermitStartAt, input.PermitEndAt, input.MinWaterLevel, input.MaxWaterLevel); err != nil {
		return model.OperationDirective{}, err
	}
	gate, err := s.gates.GetByCode(ctx, input.RelatedCode)
	if err != nil {
		return model.OperationDirective{}, fmt.Errorf("linked gate %q: %w", input.RelatedCode, err)
	}
	if !strings.EqualFold(strings.TrimSpace(input.Facility), gate.Facility) || gate.Status == string(constants.GateStateLocked) {
		return model.OperationDirective{}, fmt.Errorf("%w: linked gate is locked or belongs to another facility", ErrInvalidInput)
	}
	gateState := strings.TrimSpace(input.GateState)
	if gateState == "" {
		gateState = string(constants.GateStateClosed)
	}
	item := model.OperationDirective{
		BaseModel: model.BaseModel{
			Code: strings.ToUpper(strings.TrimSpace(input.Code)), Name: strings.TrimSpace(input.Name),
			Status: model.OperationDirectiveInitialStatus, Version: 1, Description: strings.TrimSpace(input.Description),
		},
		Facility: strings.TrimSpace(input.Facility), Owner: strings.TrimSpace(input.Owner),
		Category: strings.TrimSpace(input.Category), RiskLevel: input.RiskLevel,
		MetricValue: input.MetricValue, MetricUnit: strings.TrimSpace(input.MetricUnit),
		EffectiveAt: input.EffectiveAt.UTC(), Evidence: strings.TrimSpace(input.Evidence),
		RelatedCode: strings.ToUpper(strings.TrimSpace(input.RelatedCode)), GateState: gateState,
		PermitStartAt: input.PermitStartAt.UTC(), PermitEndAt: input.PermitEndAt.UTC(),
		MinWaterLevel: input.MinWaterLevel, MaxWaterLevel: input.MaxWaterLevel,
	}
	if err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.Create(txCtx, &item); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "create", "OperationDirective", item.ID, "", item.Status, "created 操作指令")
	}); err != nil {
		return model.OperationDirective{}, fmt.Errorf("create 操作指令: %w", err)
	}
	return item, nil
}

func (s *operationDirectiveService) Update(ctx context.Context, id uint, input dto.UpdateOperationDirective, actor, requestID string) (model.OperationDirective, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.OperationDirective{}, err
	}
	if current.Status != string(constants.DirectiveStateDraft) {
		return model.OperationDirective{}, ErrImmutableState
	}
	if err := validateOperationDirectiveBusinessFields(current.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.OperationDirective{}, err
	}
	if err := validatePermitWindow(input.PermitStartAt, input.PermitEndAt, input.MinWaterLevel, input.MaxWaterLevel); err != nil {
		return model.OperationDirective{}, err
	}
	gate, err := s.gates.GetByCode(ctx, input.RelatedCode)
	if err != nil {
		return model.OperationDirective{}, fmt.Errorf("linked gate %q: %w", input.RelatedCode, err)
	}
	if !strings.EqualFold(strings.TrimSpace(input.Facility), gate.Facility) || gate.Status == string(constants.GateStateLocked) {
		return model.OperationDirective{}, fmt.Errorf("%w: linked gate is locked or belongs to another facility", ErrInvalidInput)
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Description = strings.TrimSpace(input.Description)
	current.Facility = strings.TrimSpace(input.Facility)
	current.Owner = strings.TrimSpace(input.Owner)
	current.Category = strings.TrimSpace(input.Category)
	current.RiskLevel = input.RiskLevel
	current.MetricValue = input.MetricValue
	current.MetricUnit = strings.TrimSpace(input.MetricUnit)
	current.EffectiveAt = input.EffectiveAt.UTC()
	current.Evidence = strings.TrimSpace(input.Evidence)
	current.RelatedCode = strings.ToUpper(strings.TrimSpace(input.RelatedCode))
	current.PermitStartAt = input.PermitStartAt.UTC()
	current.PermitEndAt = input.PermitEndAt.UTC()
	current.MinWaterLevel = input.MinWaterLevel
	current.MaxWaterLevel = input.MaxWaterLevel
	if gateState := strings.TrimSpace(input.GateState); gateState != "" {
		current.GateState = gateState
	}
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.Update(txCtx, id, input.ExpectedVersion, &current); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "update", "OperationDirective", id, current.Status, current.Status, "updated business fields")
	}); err != nil {
		return model.OperationDirective{}, fmt.Errorf("update 操作指令: %w", err)
	}
	return s.repository.Get(ctx, id)
}

func (s *operationDirectiveService) Transition(ctx context.Context, id uint, input dto.TransitionRequest, actor, role, requestID string) (model.OperationDirective, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.OperationDirective{}, err
	}
	target := strings.TrimSpace(input.Status)
	if !constants.CanTransition(constants.OperationDirectiveTransitions, current.Status, target) {
		return model.OperationDirective{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, target)
	}
	if target == string(constants.DirectiveStateCompleted) {
		return model.OperationDirective{}, fmt.Errorf("%w: completion is recorded by an execution confirmation", ErrInvalidTransition)
	}
	if !directiveRoleAllowed(current.Status, target, role) {
		return model.OperationDirective{}, ErrForbidden
	}
	var gate *model.GateUnit
	var gateTarget string
	if target == string(constants.DirectiveStateExecuting) || (current.Status == string(constants.DirectiveStateExecuting) && target == string(constants.DirectiveStateAborted)) {
		linkedGate, gateErr := s.gates.GetByCode(ctx, current.RelatedCode)
		if gateErr != nil {
			return model.OperationDirective{}, fmt.Errorf("linked gate %q: %w", current.RelatedCode, gateErr)
		}
		if target == string(constants.DirectiveStateExecuting) && linkedGate.Status == string(constants.GateStateLocked) {
			return model.OperationDirective{}, fmt.Errorf("%w: locked gate cannot execute a directive", ErrInvalidInput)
		}
		if target == string(constants.DirectiveStateExecuting) {
			if err := s.checkPermitWindow(ctx, current, linkedGate, time.Now().UTC()); err != nil {
				return model.OperationDirective{}, err
			}
		}
		gate = &linkedGate
		if target == string(constants.DirectiveStateAborted) {
			gateTarget = string(constants.GateStateLocked)
		} else if linkedGate.Status != current.GateState {
			gateTarget = string(constants.GateStateMoving)
		}
		if gateTarget != "" && linkedGate.Status != gateTarget && !constants.CanTransition(constants.GateUnitTransitions, linkedGate.Status, gateTarget) {
			return model.OperationDirective{}, fmt.Errorf("%w: gate %s cannot move from %s to %s", ErrInvalidTransition, linkedGate.Code, linkedGate.Status, gateTarget)
		}
	}
	if target == string(constants.DirectiveStateApproved) {
		if current.SubmittedBy == "" || strings.EqualFold(current.SubmittedBy, actor) {
			return model.OperationDirective{}, ErrTwoPersonRequired
		}
	}
	before := current.Status
	now := time.Now().UTC()
	current.Status = target
	var approval *model.DirectiveApproval
	if target == string(constants.DirectiveStatePending) {
		current.SubmittedBy = actor
		current.SubmittedAt = &now
		approval = &model.DirectiveApproval{Stage: "submitted", Actor: actor, Role: role, RequestID: requestID,
			Reason: strings.TrimSpace(input.Reason), FromState: before, ToState: target, CreatedAt: now}
	}
	if target == string(constants.DirectiveStateApproved) {
		current.ApprovedBy = actor
		current.ApprovedAt = &now
		approval = &model.DirectiveApproval{Stage: "approved", Actor: actor, Role: role, RequestID: requestID,
			Reason: strings.TrimSpace(input.Reason), FromState: before, ToState: target, CreatedAt: now}
	}
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = now
	audit := &model.AuditLog{Actor: actor, RequestID: requestID, Action: "transition", EntityType: "OperationDirective", EntityID: id,
		BeforeState: before, AfterState: target, Detail: input.Reason, CreatedAt: now}
	if err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.TransitionWithApproval(txCtx, id, input.ExpectedVersion, &current, approval, audit); err != nil {
			return err
		}
		if gate == nil || gateTarget == "" || gate.Status == gateTarget {
			return nil
		}
		gateBefore := gate.Status
		gate.Status = gateTarget
		gate.Version++
		gate.UpdatedAt = now
		if err := s.gates.Update(txCtx, gate.ID, gate.Version-1, gate); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "directive_execution", "GateUnit", gate.ID, gateBefore, gateTarget, input.Reason)
	}); err != nil {
		return model.OperationDirective{}, fmt.Errorf("transition 操作指令: %w", err)
	}
	return s.repository.Get(ctx, id)
}

func (s *operationDirectiveService) Delete(ctx context.Context, id uint, actor, requestID string) error {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Status != string(constants.DirectiveStateDraft) {
		return ErrImmutableState
	}
	return s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.Delete(txCtx, id); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "delete", "OperationDirective", id, current.Status, "deleted", "soft deleted 操作指令")
	})
}

func directiveRoleAllowed(from, target, role string) bool {
	switch target {
	case string(constants.DirectiveStatePending):
		return role == model.RoleOperator || role == model.RoleAdmin
	case string(constants.DirectiveStateApproved):
		return role == model.RoleReviewer || role == model.RoleAdmin
	case string(constants.DirectiveStateExecuting):
		return role == model.RoleOperator || role == model.RoleAdmin
	case string(constants.DirectiveStateAborted):
		return role == model.RoleOperator || role == model.RoleReviewer || role == model.RoleAdmin
	default:
		return false
	}
}

func (s *operationDirectiveService) StatusCounts(ctx context.Context) (map[string]int64, error) {
	return s.repository.CountByStatus(ctx)
}

func validateOperationDirectiveBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}

// validatePermitWindow enforces the create/edit contract for 调度许可条件:
// the permitted time window must be ordered and the water level bounds must be
// a valid non-empty range.
func validatePermitWindow(startAt, endAt time.Time, minLevel, maxLevel float64) error {
	if startAt.IsZero() || endAt.IsZero() {
		return fmt.Errorf("%w: permit start and end times are required", ErrInvalidInput)
	}
	if !startAt.UTC().Before(endAt.UTC()) {
		return fmt.Errorf("%w: permit start time must be earlier than end time", ErrInvalidInput)
	}
	if minLevel <= 0 || maxLevel <= 0 {
		return fmt.Errorf("%w: permit water level bounds must be greater than zero", ErrInvalidInput)
	}
	if minLevel > maxLevel {
		return fmt.Errorf("%w: minimum water level must not be higher than maximum water level", ErrInvalidInput)
	}
	return nil
}

// checkPermitWindow is the runtime gate for starting an approved directive. The
// target gate belongs to a reservoir (GateUnit.RelatedCode is the reservoir
// code); execution is allowed only when the current time is inside the permit
// window and the reservoir's current water level is within the recorded
// bounds. Any rejection happens before persistence, so the directive and the
// gate are left in their original states.
func (s *operationDirectiveService) checkPermitWindow(ctx context.Context, directive model.OperationDirective, gate model.GateUnit, now time.Time) error {
	if now.Before(directive.PermitStartAt) {
		return fmt.Errorf("%w: current time %s is before permit start %s", ErrPermitWindow,
			now.Format(time.RFC3339), directive.PermitStartAt.Format(time.RFC3339))
	}
	if now.After(directive.PermitEndAt) {
		return fmt.Errorf("%w: current time %s is after permit end %s", ErrPermitWindow,
			now.Format(time.RFC3339), directive.PermitEndAt.Format(time.RFC3339))
	}
	reservoir, err := s.reservoirs.GetByCode(ctx, gate.RelatedCode)
	if err != nil {
		return fmt.Errorf("linked reservoir %q for gate %q: %w", gate.RelatedCode, gate.Code, err)
	}
	currentLevel := reservoir.MetricValue
	if currentLevel < directive.MinWaterLevel || currentLevel > directive.MaxWaterLevel {
		return fmt.Errorf("%w: reservoir %s current water level %.2f is outside permitted range [%.2f, %.2f]",
			ErrPermitWindow, reservoir.Code, currentLevel, directive.MinWaterLevel, directive.MaxWaterLevel)
	}
	return nil
}
