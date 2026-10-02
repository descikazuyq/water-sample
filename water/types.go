package water

import (
	"errors"
	"time"
)

// SampleState 表示样品在判定流程中的状态。
type SampleState string

const (
	// StatePending 待判定：已录入，尚未确认结果。
	StatePending SampleState = "pending"
	// StateConfirmed 已确认：判定结果已保存。
	StateConfirmed SampleState = "confirmed"
	// StateVoided 已作废：保留测量与已有结果，但不能再确认。
	StateVoided SampleState = "voided"
)

// Point 是登记的采样点。
type Point struct {
	Number string `json:"number"` // 唯一编号（已去掉首尾空白）
	Name   string `json:"name"`   // 名称（已去掉首尾空白）
}

// Measurement 是样品中一个项目的测量值。
type Measurement struct {
	Item  string  `json:"item"`  // 项目名称（已去掉首尾空白）
	Value float64 `json:"value"` // 测量值，必须是有限数
}

// LimitRecord 是某采样点某项目的一版上限。
type LimitRecord struct {
	PointNumber string    `json:"point_number"`
	Item        string    `json:"item"`
	Limit       float64   `json:"limit"`
	EffectiveAt time.Time `json:"effective_at"`
}

// ItemResult 是确认后每个项目的判定结果。
type ItemResult struct {
	Item        string    `json:"item"`
	Value       float64   `json:"value"`
	Limit       float64   `json:"limit"`
	EffectiveAt time.Time `json:"effective_at"`
	Exceeded    bool      `json:"exceeded"`
}

// Sample 是录入的样品。
type Sample struct {
	Number       string        `json:"number"`
	PointNumber  string        `json:"point_number"`
	SampledAt    time.Time     `json:"sampled_at"`
	Measurements []Measurement `json:"measurements"`
	State        SampleState   `json:"state"`
	Reason       string        `json:"reason,omitempty"`
	Results      []ItemResult  `json:"results,omitempty"`
	Exceeded     bool          `json:"exceeded"`
	ConfirmedAt  time.Time     `json:"confirmed_at,omitempty"`
	VoidedAt     time.Time     `json:"voided_at,omitempty"`
}

// LatestResult 是按规则选取的最近一次有效结果。
type LatestResult struct {
	Found  bool
	Sample *Sample
}

var (
	ErrClosed            = errors.New("water: store is closed")
	ErrInvalidNumber     = errors.New("water: number is empty")
	ErrInvalidName       = errors.New("water: name is empty")
	ErrInvalidItem       = errors.New("water: item is empty")
	ErrInvalidReason     = errors.New("water: reason is empty")
	ErrInvalidValue      = errors.New("water: value must be a finite number")
	ErrInvalidLimit      = errors.New("water: limit must be a finite number")
	ErrInvalidTime       = errors.New("water: required time is missing")
	ErrPointNotFound     = errors.New("water: sampling point not found")
	ErrSampleNotFound    = errors.New("water: sample not found")
	ErrDuplicatePoint    = errors.New("water: sampling point already exists")
	ErrDuplicateLimit    = errors.New("water: limit with same effective time already exists")
	ErrDuplicateItem     = errors.New("water: duplicate item in sample")
	ErrNoMeasurements    = errors.New("water: at least one measurement is required")
	ErrConflict          = errors.New("water: submitted content conflicts with existing record")
	ErrVoided            = errors.New("water: sample is voided")
	ErrNoApplicableLimit = errors.New("water: no applicable limit for measurement")
)
