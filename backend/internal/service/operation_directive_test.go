package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/config"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newDirectiveService(t *testing.T) (OperationDirectiveService, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Reservoir{}, &model.GateUnit{}, &model.OperationDirective{}, &model.DirectiveApproval{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	reservoir := model.Reservoir{BaseModel: model.BaseModel{Code: "R-TEST", Name: "右岸库区", Status: "normal", Version: 1},
		Facility: "右岸坝段", Owner: "运行一组", MetricValue: 168.2, MetricUnit: "m"}
	if err := db.Create(&reservoir).Error; err != nil {
		t.Fatalf("create test reservoir: %v", err)
	}
	gate := model.GateUnit{BaseModel: model.BaseModel{Code: "GU-TEST", Name: "右岸泄洪闸", Status: "closed", Version: 1},
		Facility: "右岸坝段", Owner: "运行一组", RelatedCode: "R-TEST"}
	if err := db.Create(&gate).Error; err != nil {
		t.Fatalf("create test gate: %v", err)
	}
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	return NewOperationDirectiveService(
		repository.NewOperationDirectiveRepository(db),
		repository.NewGateUnitRepository(db),
		repository.NewReservoirRepository(db),
		security,
	), db
}

func directiveInput(code string) dto.CreateOperationDirective {
	now := time.Now().UTC()
	return dto.CreateOperationDirective{
		Code: code, Name: "右岸泄洪闸调度", Description: "测试双人确认和状态证据",
		Facility: "右岸坝段", Owner: "运行一组", Category: "泄洪调度", RiskLevel: "high",
		MetricValue: 35, MetricUnit: "%", EffectiveAt: now.Add(time.Hour),
		Evidence: "水位 168.2m，处于许可窗口", RelatedCode: "GU-TEST", GateState: "open",
		PermitStartAt: now.Add(-2 * time.Hour), PermitEndAt: now.Add(2 * time.Hour),
		MinWaterLevel: 160.0, MaxWaterLevel: 175.0,
	}
}

func TestDirectiveRequiresIndependentReviewerAndPreservesEvidence(t *testing.T) {
	service, db := newDirectiveService(t)
	ctx := context.Background()
	created, err := service.Create(ctx, directiveInput("OD-TEST-1"), "operator", "req-create")
	if err != nil {
		t.Fatalf("create directive: %v", err)
	}
	submitted, err := service.Transition(ctx, created.ID, dto.TransitionRequest{
		Status: "pending", ExpectedVersion: created.Version, Reason: "提交水位窗口和开度计划复核",
	}, "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit directive: %v", err)
	}
	if submitted.SubmittedBy != "operator" || len(submitted.Approvals) != 1 || submitted.Approvals[0].RequestID != "req-submit" {
		t.Fatalf("submission evidence not preserved: %#v", submitted)
	}

	_, err = service.Transition(ctx, submitted.ID, dto.TransitionRequest{
		Status: "approved", ExpectedVersion: submitted.Version, Reason: "操作员不得自行批准",
	}, "operator", model.RoleOperator, "req-self-operator")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator approval should be forbidden, got %v", err)
	}
	_, err = service.Transition(ctx, submitted.ID, dto.TransitionRequest{
		Status: "approved", ExpectedVersion: submitted.Version, Reason: "同一账号不得切换角色自批",
	}, "operator", model.RoleReviewer, "req-self-reviewer")
	if !errors.Is(err, ErrTwoPersonRequired) {
		t.Fatalf("same-account approval should fail two-person rule, got %v", err)
	}

	approved, err := service.Transition(ctx, submitted.ID, dto.TransitionRequest{
		Status: "approved", ExpectedVersion: submitted.Version, Reason: "复核闸门目标、水位窗口及证据一致",
	}, "reviewer", model.RoleReviewer, "req-approve")
	if err != nil {
		t.Fatalf("reviewer approve directive: %v", err)
	}
	if approved.ApprovedBy != "reviewer" || submitted.SubmittedBy == approved.ApprovedBy || len(approved.Approvals) != 2 {
		t.Fatalf("two-person evidence invalid: %#v", approved)
	}
	if approved.Approvals[1].Stage != "approved" || approved.Approvals[1].RequestID != "req-approve" {
		t.Fatalf("approval trail invalid: %#v", approved.Approvals)
	}
	var transitions int64
	if err := db.Model(&model.AuditLog{}).Where("entity_type = ? AND entity_id = ? AND action = ?", "OperationDirective", approved.ID, "transition").Count(&transitions).Error; err != nil {
		t.Fatalf("count transition audits: %v", err)
	}
	if transitions != 2 {
		t.Fatalf("expected two transition audits, got %d", transitions)
	}

	// 复核通过后，时间落在许可时段且水位在上下限内，允许开始执行。
	executing, err := service.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "许可条件满足，开始执行",
	}, "operator", model.RoleOperator, "req-execute")
	if err != nil {
		t.Fatalf("execute within permit window: %v", err)
	}
	if executing.Status != "executing" {
		t.Fatalf("directive should be executing, got %s", executing.Status)
	}
	gate, err := repository.NewGateUnitRepository(db).GetByCode(ctx, "GU-TEST")
	if err != nil {
		t.Fatalf("reload gate: %v", err)
	}
	if gate.Status != "moving" {
		t.Fatalf("gate should start moving, got %s", gate.Status)
	}

	_, err = service.Update(ctx, approved.ID, dto.UpdateOperationDirective{ExpectedVersion: approved.Version}, "operator", "req-edit")
	if !errors.Is(err, ErrImmutableState) {
		t.Fatalf("submitted directive must be immutable, got %v", err)
	}
}

func TestDirectiveTransitionRollsBackWhenAuditCannotPersist(t *testing.T) {
	service, db := newDirectiveService(t)
	ctx := context.Background()
	created, err := service.Create(ctx, directiveInput("OD-TEST-ROLLBACK"), "operator", "req-create")
	if err != nil {
		t.Fatalf("create directive: %v", err)
	}
	if err := db.Migrator().DropTable(&model.AuditLog{}); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	_, err = service.Transition(ctx, created.ID, dto.TransitionRequest{
		Status: "pending", ExpectedVersion: created.Version, Reason: "应与审计失败一起回滚",
	}, "operator", model.RoleOperator, "req-rollback")
	if err == nil {
		t.Fatal("transition should fail when audit cannot persist")
	}
	stored, getErr := service.Get(ctx, created.ID)
	if getErr != nil {
		t.Fatalf("reload rolled-back directive: %v", getErr)
	}
	if stored.Status != "draft" || stored.Version != created.Version || len(stored.Approvals) != 0 {
		t.Fatalf("transition was not fully rolled back: %#v", stored)
	}
}

func TestDirectiveCreateRollsBackWhenAuditCannotPersist(t *testing.T) {
	service, db := newDirectiveService(t)
	if err := db.Migrator().DropTable(&model.AuditLog{}); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	_, err := service.Create(context.Background(), directiveInput("OD-CREATE-ROLLBACK"), "operator", "req-create-rollback")
	if err == nil {
		t.Fatal("create should fail when audit cannot persist")
	}
	var count int64
	if err := db.Model(&model.OperationDirective{}).Where("code = ?", "OD-CREATE-ROLLBACK").Count(&count).Error; err != nil {
		t.Fatalf("count directives: %v", err)
	}
	if count != 0 {
		t.Fatalf("directive persisted without audit: count=%d", count)
	}
}

func TestDirectiveRejectsInvalidPermitWindowOnCreate(t *testing.T) {
	svc, _ := newDirectiveService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	invalidOrder := directiveInput("OD-PERMIT-TIME")
	invalidOrder.PermitStartAt = now.Add(2 * time.Hour)
	invalidOrder.PermitEndAt = now.Add(-2 * time.Hour)
	if _, err := svc.Create(ctx, invalidOrder, "operator", "req-permit-time"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reversed permit window should be rejected, got %v", err)
	}

	invalidBounds := directiveInput("OD-PERMIT-LEVEL")
	invalidBounds.MinWaterLevel = 180.0
	invalidBounds.MaxWaterLevel = 160.0
	if _, err := svc.Create(ctx, invalidBounds, "operator", "req-permit-level"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reversed water level bounds should be rejected, got %v", err)
	}

	zeroBounds := directiveInput("OD-PERMIT-ZERO")
	zeroBounds.MinWaterLevel = 0
	zeroBounds.MaxWaterLevel = 160.0
	if _, err := svc.Create(ctx, zeroBounds, "operator", "req-permit-zero"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero water level bound should be rejected, got %v", err)
	}
}

func approveDirective(t *testing.T, svc OperationDirectiveService, code string) model.OperationDirective {
	t.Helper()
	ctx := context.Background()
	created, err := svc.Create(ctx, directiveInput(code), "operator", "req-create")
	if err != nil {
		t.Fatalf("create directive: %v", err)
	}
	submitted, err := svc.Transition(ctx, created.ID, dto.TransitionRequest{
		Status: "pending", ExpectedVersion: created.Version, Reason: "提交复核",
	}, "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit directive: %v", err)
	}
	approved, err := svc.Transition(ctx, submitted.ID, dto.TransitionRequest{
		Status: "approved", ExpectedVersion: submitted.Version, Reason: "复核通过",
	}, "reviewer", model.RoleReviewer, "req-approve")
	if err != nil {
		t.Fatalf("approve directive: %v", err)
	}
	return approved
}

func TestDirectiveExecutionBlockedOutsidePermitTime(t *testing.T) {
	svc, db := newDirectiveService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, directiveInput("OD-TIME-BLOCK"), "operator", "req-create")
	if err != nil {
		t.Fatalf("create directive: %v", err)
	}
	now := time.Now().UTC()
	// 许可时段已经结束：即便复核通过也不得开工。
	created.PermitStartAt = now.Add(-4 * time.Hour)
	created.PermitEndAt = now.Add(-2 * time.Hour)
	if err := db.Model(&model.OperationDirective{}).Where("id = ?", created.ID).
		Updates(map[string]any{"permit_start_at": created.PermitStartAt, "permit_end_at": created.PermitEndAt}).Error; err != nil {
		t.Fatalf("shift permit window: %v", err)
	}
	approved := approveDirectiveFrom(t, svc, created.ID)

	_, err = svc.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "尝试在许可时段外执行",
	}, "operator", model.RoleOperator, "req-execute-blocked")
	if !errors.Is(err, ErrPermitWindow) {
		t.Fatalf("execution outside permit time should be rejected, got %v", err)
	}
	stored, getErr := svc.Get(ctx, approved.ID)
	if getErr != nil {
		t.Fatalf("reload directive: %v", getErr)
	}
	if stored.Status != "approved" || stored.Version != approved.Version {
		t.Fatalf("directive state must remain unchanged after rejection: %#v", stored)
	}
	gate, err := repository.NewGateUnitRepository(db).GetByCode(ctx, "GU-TEST")
	if err != nil {
		t.Fatalf("reload gate: %v", err)
	}
	if gate.Status != "closed" || gate.Version != 1 {
		t.Fatalf("gate state must remain unchanged after rejection: %#v", gate)
	}
}

// approveDirectiveFrom moves a previously created directive to approved without
// re-creating it, so callers can first mutate permit conditions in the database.
func approveDirectiveFrom(t *testing.T, svc OperationDirectiveService, id uint) model.OperationDirective {
	t.Helper()
	ctx := context.Background()
	current, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("load directive: %v", err)
	}
	submitted, err := svc.Transition(ctx, id, dto.TransitionRequest{
		Status: "pending", ExpectedVersion: current.Version, Reason: "提交复核",
	}, "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit directive: %v", err)
	}
	approved, err := svc.Transition(ctx, id, dto.TransitionRequest{
		Status: "approved", ExpectedVersion: submitted.Version, Reason: "复核通过",
	}, "reviewer", model.RoleReviewer, "req-approve")
	if err != nil {
		t.Fatalf("approve directive: %v", err)
	}
	return approved
}

func TestDirectiveExecutionBlockedWhenWaterLevelOutOfBounds(t *testing.T) {
	svc, db := newDirectiveService(t)
	ctx := context.Background()
	approved := approveDirective(t, svc, "OD-LEVEL-BLOCK")

	// 库区当前水位超出指令许可上限，闸门与指令必须保持原状态。
	if err := db.Model(&model.Reservoir{}).Where("code = ?", "R-TEST").
		Update("metric_value", 180.0).Error; err != nil {
		t.Fatalf("raise reservoir level: %v", err)
	}
	_, err := svc.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "尝试在水位超限时执行",
	}, "operator", model.RoleOperator, "req-execute-level-blocked")
	if !errors.Is(err, ErrPermitWindow) {
		t.Fatalf("execution outside water level bounds should be rejected, got %v", err)
	}
	stored, getErr := svc.Get(ctx, approved.ID)
	if getErr != nil {
		t.Fatalf("reload directive: %v", getErr)
	}
	if stored.Status != "approved" {
		t.Fatalf("directive must remain approved, got %s", stored.Status)
	}
	gate, err := repository.NewGateUnitRepository(db).GetByCode(ctx, "GU-TEST")
	if err != nil {
		t.Fatalf("reload gate: %v", err)
	}
	if gate.Status != "closed" {
		t.Fatalf("gate must remain closed, got %s", gate.Status)
	}

	// 水位回到许可区间后，同一条已批准指令可以正常开工。
	if err := db.Model(&model.Reservoir{}).Where("code = ?", "R-TEST").
		Update("metric_value", 168.2).Error; err != nil {
		t.Fatalf("restore reservoir level: %v", err)
	}
	executing, err := svc.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "水位恢复，开始执行",
	}, "operator", model.RoleOperator, "req-execute-ok")
	if err != nil {
		t.Fatalf("execution within bounds should succeed, got %v", err)
	}
	if executing.Status != "executing" {
		t.Fatalf("directive should be executing, got %s", executing.Status)
	}
}
