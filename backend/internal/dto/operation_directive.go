package dto

import "time"

// CreateOperationDirective is the public write contract for 操作指令. Status is deliberately
// omitted so callers cannot bypass the service state machine.
type CreateOperationDirective struct {
	Code        string    `json:"code" binding:"required,min=2,max=64"`
	Name        string    `json:"name" binding:"required,min=2,max=160"`
	Description string    `json:"description" binding:"max=1000"`
	Facility    string    `json:"facility" binding:"required,max=120"`
	Owner       string    `json:"owner" binding:"required,max=120"`
	Category    string    `json:"category" binding:"required,max=80"`
	RiskLevel   string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue float64   `json:"metricValue"`
	MetricUnit  string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt time.Time `json:"effectiveAt" binding:"required"`
	Evidence    string    `json:"evidence" binding:"max=2000"`
	RelatedCode string    `json:"relatedCode" binding:"required,min=2,max=64"`
	GateState   string    `json:"gateState" binding:"omitempty,oneof=open closed locked"`
	// 调度许可条件（建单时必须录入，业务规则在 service 层校验）：
	// 许可开始时间必须早于结束时间；最低水位必须不高于最高水位。
	PermitStartAt time.Time `json:"permitStartAt" binding:"required"`
	PermitEndAt   time.Time `json:"permitEndAt" binding:"required"`
	MinWaterLevel float64   `json:"minWaterLevel"`
	MaxWaterLevel float64   `json:"maxWaterLevel"`
}

type UpdateOperationDirective struct {
	ExpectedVersion uint      `json:"expectedVersion" binding:"required"`
	Name            string    `json:"name" binding:"required,min=2,max=160"`
	Description     string    `json:"description" binding:"max=1000"`
	Facility        string    `json:"facility" binding:"required,max=120"`
	Owner           string    `json:"owner" binding:"required,max=120"`
	Category        string    `json:"category" binding:"required,max=80"`
	RiskLevel       string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue     float64   `json:"metricValue"`
	MetricUnit      string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt     time.Time `json:"effectiveAt" binding:"required"`
	Evidence        string    `json:"evidence" binding:"max=2000"`
	RelatedCode     string    `json:"relatedCode" binding:"required,min=2,max=64"`
	GateState       string    `json:"gateState" binding:"omitempty,oneof=open closed locked"`
	// 调度许可条件在草稿阶段可随业务字段一起修改，规则与建单一致。
	PermitStartAt time.Time `json:"permitStartAt" binding:"required"`
	PermitEndAt   time.Time `json:"permitEndAt" binding:"required"`
	MinWaterLevel float64   `json:"minWaterLevel"`
	MaxWaterLevel float64   `json:"maxWaterLevel"`
}
