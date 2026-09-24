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
	reservoir := model.Reservoir{BaseModel: model.BaseModel{Code: "RS-TEST", Name: "上游库区", Status: "normal", Version: 1}, Facility: "右岸坝段", Owner: "运行一组", MetricValue: 168.2, MetricUnit: "m"}
	if err := db.Create(&reservoir).Error; err != nil {
		t.Fatalf("create test reservoir: %v", err)
	}
	gate := model.GateUnit{BaseModel: model.BaseModel{Code: "GU-TEST", Name: "右岸泄洪闸", Status: "closed", Version: 1}, Facility: "右岸坝段", Owner: "运行一组", RelatedCode: "RS-TEST"}
	if err := db.Create(&gate).Error; err != nil {
		t.Fatalf("create test gate: %v", err)
	}
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	return NewOperationDirectiveService(repository.NewOperationDirectiveRepository(db), repository.NewGateUnitRepository(db), repository.NewReservoirRepository(db), security), db
}

func directiveInput(code string) dto.CreateOperationDirective {
	return dto.CreateOperationDirective{
		Code: code, Name: "右岸泄洪闸调度", Description: "测试双人确认和状态证据",
		Facility: "右岸坝段", Owner: "运行一组", Category: "泄洪调度", RiskLevel: "high",
		MetricValue: 35, MetricUnit: "%", EffectiveAt: time.Now().UTC().Add(time.Hour),
		Evidence: "水位 168.2m，处于许可窗口", RelatedCode: "GU-TEST", GateState: "closed",
		PermitStartAt: time.Now().UTC().Add(-time.Hour), PermitEndAt: time.Now().UTC().Add(24 * time.Hour),
		MinWaterLevel: 160, MaxWaterLevel: 175,
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
	if approved.ApprovedBy != "reviewer" || approved.SubmittedBy == approved.ApprovedBy || len(approved.Approvals) != 2 {
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

func approveDirectiveForPermitTest(t *testing.T, service OperationDirectiveService, created model.OperationDirective) model.OperationDirective {
	t.Helper()
	ctx := context.Background()
	submitted, err := service.Transition(ctx, created.ID, dto.TransitionRequest{
		Status: "pending", ExpectedVersion: created.Version, Reason: "提交许可时段与水位上下限复核",
	}, "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit directive: %v", err)
	}
	approved, err := service.Transition(ctx, submitted.ID, dto.TransitionRequest{
		Status: "approved", ExpectedVersion: submitted.Version, Reason: "复核通过",
	}, "reviewer", model.RoleReviewer, "req-approve")
	if err != nil {
		t.Fatalf("approve directive: %v", err)
	}
	return approved
}

func TestDirectiveRejectsInvalidPermitConditionsOnCreate(t *testing.T) {
	service, db := newDirectiveService(t)
	ctx := context.Background()

	invalidWindow := directiveInput("OD-TEST-WINDOW")
	invalidWindow.PermitStartAt = time.Now().UTC().Add(time.Hour)
	invalidWindow.PermitEndAt = time.Now().UTC().Add(-time.Hour)
	if _, err := service.Create(ctx, invalidWindow, "operator", "req-create-window"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("inverted permit window must be rejected at creation, got %v", err)
	}

	invalidLevel := directiveInput("OD-TEST-LEVEL")
	invalidLevel.MinWaterLevel = 180
	invalidLevel.MaxWaterLevel = 160
	if _, err := service.Create(ctx, invalidLevel, "operator", "req-create-level"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("inverted water level bounds must be rejected at creation, got %v", err)
	}

	var count int64
	if err := db.Model(&model.OperationDirective{}).Count(&count).Error; err != nil {
		t.Fatalf("count directives: %v", err)
	}
	if count != 0 {
		t.Fatalf("directives with invalid permit conditions must not persist, count=%d", count)
	}
}

func TestDirectiveExecutionRequiresPermitWindowAndWaterLevel(t *testing.T) {
	service, db := newDirectiveService(t)
	ctx := context.Background()
	assertUntouched := func(directiveID uint, approvedVersion uint) {
		t.Helper()
		stored, getErr := service.Get(ctx, directiveID)
		if getErr != nil {
			t.Fatalf("reload denied directive: %v", getErr)
		}
		if stored.Status != "approved" || stored.Version != approvedVersion {
			t.Fatalf("denied directive must keep its state: %#v", stored)
		}
		var gate model.GateUnit
		if err := db.First(&gate, "code = ?", "GU-TEST").Error; err != nil {
			t.Fatalf("load linked gate: %v", err)
		}
		if gate.Status != "closed" {
			t.Fatalf("gate must remain closed after denied execution, got %s", gate.Status)
		}
	}

	expired := directiveInput("OD-TEST-EXPIRED")
	expired.PermitStartAt = time.Now().UTC().Add(-2 * time.Hour)
	expired.PermitEndAt = time.Now().UTC().Add(-time.Hour)
	created, err := service.Create(ctx, expired, "operator", "req-create-expired")
	if err != nil {
		t.Fatalf("create expired-window directive: %v", err)
	}
	approved := approveDirectiveForPermitTest(t, service, created)
	if _, err := service.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "许可时段已结束仍试图开工",
	}, "operator", model.RoleOperator, "req-execute-expired"); !errors.Is(err, ErrPermitCondition) {
		t.Fatalf("execution outside the permit window must be denied, got %v", err)
	}
	assertUntouched(approved.ID, approved.Version)

	levelOut := directiveInput("OD-TEST-LEVEL-OUT")
	levelOut.MinWaterLevel = 200
	levelOut.MaxWaterLevel = 210
	created, err = service.Create(ctx, levelOut, "operator", "req-create-level-out")
	if err != nil {
		t.Fatalf("create out-of-level directive: %v", err)
	}
	approved = approveDirectiveForPermitTest(t, service, created)
	if _, err := service.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "水位超出许可上下限仍试图开工",
	}, "operator", model.RoleOperator, "req-execute-level-out"); !errors.Is(err, ErrPermitCondition) {
		t.Fatalf("execution with water level out of bounds must be denied, got %v", err)
	}
	assertUntouched(approved.ID, approved.Version)

	permitted := directiveInput("OD-TEST-PERMITTED")
	permitted.GateState = "open"
	created, err = service.Create(ctx, permitted, "operator", "req-create-permitted")
	if err != nil {
		t.Fatalf("create permitted directive: %v", err)
	}
	approved = approveDirectiveForPermitTest(t, service, created)
	executing, err := service.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "时间与水位均满足许可条件，开始执行",
	}, "operator", model.RoleOperator, "req-execute-permitted")
	if err != nil {
		t.Fatalf("execution within permit window and water level bounds must pass: %v", err)
	}
	if executing.Status != "executing" {
		t.Fatalf("directive should be executing, got %s", executing.Status)
	}
	var gate model.GateUnit
	if err := db.First(&gate, "code = ?", "GU-TEST").Error; err != nil {
		t.Fatalf("load linked gate: %v", err)
	}
	if gate.Status != "moving" {
		t.Fatalf("gate should enter moving once permitted execution starts, got %s", gate.Status)
	}
}
