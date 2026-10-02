package water

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const dataFileName = "water-sample.json"

// Store 是本地取样判定的数据存储。所有变更都在通过校验后原子写入磁盘，
// 因此被拒绝的操作不会留下任何新增或变更记录。
type Store struct {
	mu     sync.Mutex
	dir    string
	data   *persisted
	closed bool
}

type persisted struct {
	Points  []Point       `json:"points"`
	Limits  []LimitRecord `json:"limits"`
	Samples []*Sample     `json:"samples"`

	pointByNumber map[string]int
	sampleByNumber map[string]int
}

func newPersisted() *persisted {
	return &persisted{
		pointByNumber:  map[string]int{},
		sampleByNumber: map[string]int{},
	}
}

func (p *persisted) rebuild() {
	p.pointByNumber = make(map[string]int, len(p.Points))
	for i, pt := range p.Points {
		p.pointByNumber[pt.Number] = i
	}
	p.sampleByNumber = make(map[string]int, len(p.Samples))
	for i, smp := range p.Samples {
		p.sampleByNumber[smp.Number] = i
	}
}

// Open 打开（必要时创建）一个位于 dir 的本地存储。
// 不同目录的数据互不混用；关闭后重新打开仍能查看原有记录。
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, data: newPersisted()}
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close 关闭存储。关闭后再调用任何方法都会返回 ErrClosed。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.saveLocked()
}

func (s *Store) loadLocked() error {
	b, err := os.ReadFile(filepath.Join(s.dir, dataFileName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, s.data); err != nil {
		return err
	}
	s.data.rebuild()
	return nil
}

// saveLocked 先写临时文件再原子改名，避免半写状态被重新打开时读到。
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".water-sample-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(b); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, dataFileName)); err != nil {
		os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ---- 校验辅助 ----

func clean(s string) (string, bool) {
	t := strings.TrimSpace(s)
	return t, t != ""
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func validTime(t time.Time) bool { return !t.IsZero() }

// ---- 采样点 ----

// RegisterPoint 登记采样点。编号全局唯一，编号或名称去掉首尾空白后为空则拒绝。
func (s *Store) RegisterPoint(number, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	n, ok := clean(number)
	if !ok {
		return ErrInvalidNumber
	}
	nm, ok := clean(name)
	if !ok {
		return ErrInvalidName
	}
	if _, exists := s.data.pointByNumber[n]; exists {
		return ErrDuplicatePoint
	}
	s.data.Points = append(s.data.Points, Point{Number: n, Name: nm})
	s.data.pointByNumber[n] = len(s.data.Points) - 1
	return s.saveLocked()
}

// GetPoint 返回编号对应的采样点。
func (s *Store) GetPoint(number string) (Point, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Point{}, ErrClosed
	}
	n, ok := clean(number)
	if !ok {
		return Point{}, ErrInvalidNumber
	}
	i, exists := s.data.pointByNumber[n]
	if !exists {
		return Point{}, ErrPointNotFound
	}
	return s.data.Points[i], nil
}

// ListPoints 返回全部采样点，按编号升序。
func (s *Store) ListPoints() ([]Point, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	out := append([]Point(nil), s.data.Points...)
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// ---- 上限 ----

// SetLimit 为已登记采样点的某个项目登记一版上限。
// 同一采样点、同一项目可保存多版，允许乱序补录；生效时间按时刻比较，
// 同一时刻（即使时区不同）的第二版会被拒绝。
func (s *Store) SetLimit(pointNumber, item string, limit float64, effectiveAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	pn, ok := clean(pointNumber)
	if !ok {
		return ErrInvalidNumber
	}
	it, ok := clean(item)
	if !ok {
		return ErrInvalidItem
	}
	if !finite(limit) {
		return ErrInvalidLimit
	}
	if !validTime(effectiveAt) {
		return ErrInvalidTime
	}
	if _, exists := s.data.pointByNumber[pn]; !exists {
		return ErrPointNotFound
	}
	for _, l := range s.data.Limits {
		if l.PointNumber == pn && l.Item == it && l.EffectiveAt.Equal(effectiveAt) {
			return ErrDuplicateLimit
		}
	}
	s.data.Limits = append(s.data.Limits, LimitRecord{
		PointNumber: pn, Item: it, Limit: limit, EffectiveAt: effectiveAt,
	})
	return s.saveLocked()
}

// ListLimits 返回某采样点某项目的全部上限版本，按生效时间从晚到早排列。
func (s *Store) ListLimits(pointNumber, item string) ([]LimitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	pn, ok := clean(pointNumber)
	if !ok {
		return nil, ErrInvalidNumber
	}
	it, ok := clean(item)
	if !ok {
		return nil, ErrInvalidItem
	}
	if _, exists := s.data.pointByNumber[pn]; !exists {
		return nil, ErrPointNotFound
	}
	var out []LimitRecord
	for _, l := range s.data.Limits {
		if l.PointNumber == pn && l.Item == it {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EffectiveAt.After(out[j].EffectiveAt) })
	return out, nil
}

// latestLimit 返回采样时刻或之前生效的最近一版上限。
func (s *Store) latestLimit(pointNumber, item string, sampledAt time.Time) (float64, time.Time, bool) {
	var best *LimitRecord
	for i := range s.data.Limits {
		l := &s.data.Limits[i]
		if l.PointNumber != pointNumber || l.Item != item {
			continue
		}
		if l.EffectiveAt.After(sampledAt) {
			continue
		}
		if best == nil || l.EffectiveAt.After(best.EffectiveAt) {
			best = l
		}
	}
	if best == nil {
		return 0, time.Time{}, false
	}
	return best.Limit, best.EffectiveAt, true
}

// ---- 样品 ----

// SubmitSample 录入样品。样品先处于待判定状态；同一份样品的项目不能重复，
// 录入后不能增删或替换测量。同一编号重复提交且内容相同（项目顺序不同不算变化）
// 时返回原样品；任一内容不同则拒绝整个提交。
func (s *Store) SubmitSample(number, pointNumber string, sampledAt time.Time, measurements []Measurement) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	n, ok := clean(number)
	if !ok {
		return nil, ErrInvalidNumber
	}
	pn, ok := clean(pointNumber)
	if !ok {
		return nil, ErrInvalidNumber
	}
	if !validTime(sampledAt) {
		return nil, ErrInvalidTime
	}
	if _, exists := s.data.pointByNumber[pn]; !exists {
		return nil, ErrPointNotFound
	}
	if len(measurements) == 0 {
		return nil, ErrNoMeasurements
	}
	ms := make([]Measurement, 0, len(measurements))
	seen := make(map[string]bool, len(measurements))
	for _, m := range measurements {
		it, ok := clean(m.Item)
		if !ok {
			return nil, ErrInvalidItem
		}
		if !finite(m.Value) {
			return nil, ErrInvalidValue
		}
		if seen[it] {
			return nil, ErrDuplicateItem
		}
		seen[it] = true
		ms = append(ms, Measurement{Item: it, Value: m.Value})
	}

	if idx, exists := s.data.sampleByNumber[n]; exists {
		existing := s.data.Samples[idx]
		if sameSubmission(existing, pn, sampledAt, ms) {
			return copySample(existing), nil
		}
		return nil, ErrConflict
	}

	smp := &Sample{
		Number:       n,
		PointNumber:  pn,
		SampledAt:    sampledAt,
		Measurements: ms,
		State:        StatePending,
	}
	s.data.Samples = append(s.data.Samples, smp)
	s.data.sampleByNumber[n] = len(s.data.Samples) - 1
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return copySample(smp), nil
}

func sameSubmission(smp *Sample, pointNumber string, sampledAt time.Time, ms []Measurement) bool {
	if smp.PointNumber != pointNumber || !smp.SampledAt.Equal(sampledAt) {
		return false
	}
	if len(smp.Measurements) != len(ms) {
		return false
	}
	have := make(map[string]float64, len(smp.Measurements))
	for _, m := range smp.Measurements {
		have[m.Item] = m.Value
	}
	for _, m := range ms {
		v, ok := have[m.Item]
		if !ok || v != m.Value {
			return false
		}
	}
	return true
}

// ConfirmSample 确认样品：为每个项目选择生效时间不晚于采样时间的最近一版上限
// （采样时间恰好等于生效时间时使用新值）。测量值大于上限才算超标，等于上限达标。
// 只要有一个项目找不到适用上限，本次确认拒绝，样品保持待判定，不留下部分结论；
// 补齐限值后可再次确认。已确认的样品重复确认直接返回已保存结果，之后新增或补录
// 限值不会改变已确认结果。已作废的样品不能确认。
func (s *Store) ConfirmSample(number string) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	n, ok := clean(number)
	if !ok {
		return nil, ErrInvalidNumber
	}
	idx, exists := s.data.sampleByNumber[n]
	if !exists {
		return nil, ErrSampleNotFound
	}
	smp := s.data.Samples[idx]
	if smp.State == StateVoided {
		return nil, ErrVoided
	}
	if smp.State == StateConfirmed {
		return copySample(smp), nil
	}

	// 先在临时结果上完成全部判定；任何项目缺限值都整体拒绝，不写入半成品结论。
	results := make([]ItemResult, len(smp.Measurements))
	exceeded := false
	for i, m := range smp.Measurements {
		limit, eff, ok := s.latestLimit(smp.PointNumber, m.Item, smp.SampledAt)
		if !ok {
			return nil, ErrNoApplicableLimit
		}
		ex := m.Value > limit
		if ex {
			exceeded = true
		}
		results[i] = ItemResult{
			Item: m.Item, Value: m.Value, Limit: limit, EffectiveAt: eff, Exceeded: ex,
		}
	}

	smp.Results = results
	smp.Exceeded = exceeded
	smp.State = StateConfirmed
	smp.ConfirmedAt = time.Now()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return copySample(smp), nil
}

// VoidSample 作废样品。原因去掉首尾空白后不能为空。待判定和已确认的样品都能作废；
// 作废后保留测量、原因和已有结果，但不能再确认。重复提交处理后相同的原因返回原记录，
// 不同原因则拒绝。
func (s *Store) VoidSample(number, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	n, ok := clean(number)
	if !ok {
		return ErrInvalidNumber
	}
	r, ok := clean(reason)
	if !ok {
		return ErrInvalidReason
	}
	idx, exists := s.data.sampleByNumber[n]
	if !exists {
		return ErrSampleNotFound
	}
	smp := s.data.Samples[idx]
	if smp.State == StateVoided {
		if smp.Reason == r {
			return nil
		}
		return ErrConflict
	}
	smp.State = StateVoided
	smp.Reason = r
	smp.VoidedAt = time.Now()
	return s.saveLocked()
}

// GetSample 返回编号对应的样品。
func (s *Store) GetSample(number string) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	n, ok := clean(number)
	if !ok {
		return nil, ErrInvalidNumber
	}
	idx, exists := s.data.sampleByNumber[n]
	if !exists {
		return nil, ErrSampleNotFound
	}
	return copySample(s.data.Samples[idx]), nil
}

// ListSamples 返回某采样点的全部样品，按采样时间从晚到早排列；
// 同一时刻按样品编号升序。
func (s *Store) ListSamples(pointNumber string) ([]*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	pn, ok := clean(pointNumber)
	if !ok {
		return nil, ErrInvalidNumber
	}
	if _, exists := s.data.pointByNumber[pn]; !exists {
		return nil, ErrPointNotFound
	}
	var out []*Sample
	for _, smp := range s.data.Samples {
		if smp.PointNumber == pn {
			out = append(out, copySample(smp))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.SampledAt.Equal(b.SampledAt) {
			return a.SampledAt.After(b.SampledAt)
		}
		return a.Number < b.Number
	})
	return out, nil
}

// LatestResult 返回某采样点最近一次有效结果：在已确认且未作废的样品中，
// 按采样时间从晚到早、同一时刻按编号升序选取；跳过待判定与作废样品。
// 没有已确认且未作废的样品时返回 Found=false（明确无结果）。
func (s *Store) LatestResult(pointNumber string) (*LatestResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	pn, ok := clean(pointNumber)
	if !ok {
		return nil, ErrInvalidNumber
	}
	if _, exists := s.data.pointByNumber[pn]; !exists {
		return nil, ErrPointNotFound
	}
	var best *Sample
	for _, smp := range s.data.Samples {
		if smp.PointNumber != pn || smp.State != StateConfirmed {
			continue
		}
		if best == nil || later(smp, best) {
			best = smp
		}
	}
	if best == nil {
		return &LatestResult{Found: false}, nil
	}
	return &LatestResult{Found: true, Sample: copySample(best)}, nil
}

func later(a, b *Sample) bool {
	if !a.SampledAt.Equal(b.SampledAt) {
		return a.SampledAt.After(b.SampledAt)
	}
	return a.Number < b.Number
}

func copySample(smp *Sample) *Sample {
	if smp == nil {
		return nil
	}
	c := *smp
	c.Measurements = append([]Measurement(nil), smp.Measurements...)
	c.Results = append([]ItemResult(nil), smp.Results...)
	return &c
}
