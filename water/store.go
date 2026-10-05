package water

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Status 表示样品所处的判定状态。
type Status string

const (
	// StatusPending 样品已录入，尚未确认判定结果。
	StatusPending Status = "pending"
	// StatusConfirmed 样品已确认，判定结果已保存。
	StatusConfirmed Status = "confirmed"
	// StatusVoided 样品已作废，保留记录但不能再确认。
	StatusVoided Status = "voided"
)

var (
	ErrClosed             = errors.New("water: 数据存放已关闭")
	ErrEmptyField         = errors.New("water: 编号、名称或项目去掉首尾空白后不能为空")
	ErrDuplicatePoint     = errors.New("water: 采样点编号已登记")
	ErrUnknownPoint       = errors.New("water: 采样点未登记")
	ErrInvalidValue       = errors.New("water: 数值只接受有限数")
	ErrInvalidTime        = errors.New("water: 采样时间或生效时间缺失")
	ErrDuplicateLimitTime = errors.New("water: 同一采样点同一项目已存在相同生效时间的上限")
	ErrUnknownSample      = errors.New("water: 样品编号不存在")
	ErrSampleConflict     = errors.New("water: 同一编号提交的样品内容不一致")
	ErrDuplicateItem      = errors.New("water: 同一份样品的测量项目重复")
	ErrNoMeasurements     = errors.New("water: 样品至少需要一个项目的测量值")
	ErrMissingLimit       = errors.New("water: 存在找不到适用上限的测量项目")
	ErrVoided             = errors.New("water: 样品已作废，不能再确认")
	ErrVoidReasonConflict = errors.New("water: 作废原因与已有记录不一致")
	ErrInvalidText        = errors.New("water: 采样点编号、采样点名称、样品编号或项目名不是合法的 UTF-8 文本")
)

// SamplingPoint 是按编号唯一登记的采样点。
type SamplingPoint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Limit 是某采样点某测量项目的一版数值上限。
type Limit struct {
	PointID   string    `json:"pointId"`
	Item      string    `json:"item"`
	Value     float64   `json:"value"`
	Effective time.Time `json:"effective"`
}

// Measurement 是样品中一个项目的测量值。
type Measurement struct {
	Item  string  `json:"item"`
	Value float64 `json:"value"`
}

// ItemResult 是确认后单个项目的判定结果。
type ItemResult struct {
	Item           string    `json:"item"`
	Value          float64   `json:"value"`
	Limit          float64   `json:"limit"`
	LimitEffective time.Time `json:"limitEffective"`
	Exceeded       bool      `json:"exceeded"`
}

// Sample 是一份从录入、确认到作废的样品记录。
type Sample struct {
	ID           string        `json:"id"`
	PointID      string        `json:"pointId"`
	SampledAt    time.Time     `json:"sampledAt"`
	Measurements []Measurement `json:"measurements"`
	Status       Status        `json:"status"`
	Results      []ItemResult  `json:"results,omitempty"`
	Exceeded     bool          `json:"exceeded"`
	VoidReason   string        `json:"voidReason,omitempty"`
}

// diskState 是落盘的完整数据。
type diskState struct {
	Points  map[string]SamplingPoint `json:"points"`
	Limits  map[string][]Limit       `json:"limits"` // 键仅为落盘占位，读取时按每条限值自身的采样点与项目重新分组
	Samples map[string]*Sample       `json:"samples"`
}

// limitGroup 用两个字段各自标识一组限值，避免把包含 U+0000 的文本
// 用单字符拼接成键时不同组合撞成同一组。
type limitGroup struct {
	pointID string
	item    string
}

// Store 是绑定到某个本地目录的取样判定数据存放。
// 不同目录的数据互不混用；关闭后重新打开仍能查看原有记录。
type Store struct {
	mu      sync.Mutex
	dir     string
	file    string
	closed  bool
	points  map[string]SamplingPoint
	limits  map[limitGroup][]Limit // 按生效时间升序
	samples map[string]*Sample
}

// Open 打开（必要时创建）dir 下的本地数据存放。
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("%w: 数据目录", ErrEmptyField)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:     dir,
		file:    filepath.Join(dir, "water-data.json"),
		points:  map[string]SamplingPoint{},
		limits:  map[limitGroup][]Limit{},
		samples: map[string]*Sample{},
	}
	data, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var st diskState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("water: 读取数据文件失败: %w", err)
	}
	if st.Points != nil {
		s.points = st.Points
	}
	if st.Limits != nil {
		// 不按落盘键分组：键可能是旧版本用单字符拼接生成的，包含 U+0000 的
		// 不同（采样点，项目）组合会共用同一个键。以每条限值自身的两个字段为准，
		// 让曾经被错误合并的组在重新打开时也能各自归位。
		for _, versions := range st.Limits {
			for _, lim := range versions {
				g := limitGroup{pointID: lim.PointID, item: lim.Item}
				s.limits[g] = append(s.limits[g], lim)
			}
		}
		for g, versions := range s.limits {
			sort.Slice(versions, func(i, j int) bool { return versions[i].Effective.Before(versions[j].Effective) })
			s.limits[g] = versions
		}
	}
	if st.Samples != nil {
		s.samples = st.Samples
	}
	return s, nil
}

// Close 关闭数据存放。已保存的记录已落盘，重新 Open 同一目录即可查看。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return nil
}

func clean(s string) string { return strings.TrimSpace(s) }

// diskLimitKey 生成仅用于落盘 JSON 的分组键。带上两个字段的长度前缀，
// 任何包含 U+0000 的文本组合都不会撞键；读取时并不依赖该键分组。
func diskLimitKey(pointID, item string) string {
	return strconv.Itoa(len(pointID)) + ":" + pointID + strconv.Itoa(len(item)) + ":" + item
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// invalidUTF8 报告文本是否无法作为合法 UTF-8 原样保存。必须在任何处理之前检查
// 提交的原始文本：落盘 JSON 会把非法字节（孤立的 0xFF、0xFE、不完整的多字节
// 序列等）静默替换成 U+FFFD，返回记录与保存内容会因此对不上，两个不同编号也
// 可能变成同一文本。真正的 U+FFFD（“�”）以及中文、U+0000 等都是合法字符，
// 不在拒绝之列；只在去首尾空白之外不做任何字符替换或归一化。
func invalidUTF8(s string) bool { return !utf8.ValidString(s) }

func (s *Store) checkOpenLocked() error {
	if s.closed {
		return ErrClosed
	}
	return nil
}

// persistLocked 把全部数据原子写回数据文件。只在成功变更后调用，
// 被拒绝的操作不会走到这里，因此不会留下新增或变更记录。
func (s *Store) persistLocked() error {
	diskLimits := make(map[string][]Limit, len(s.limits))
	for g, versions := range s.limits {
		diskLimits[diskLimitKey(g.pointID, g.item)] = versions
	}
	data, err := json.MarshalIndent(diskState{
		Points:  s.points,
		Limits:  diskLimits,
		Samples: s.samples,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

// RegisterPoint 登记采样点。编号、名称去掉首尾空白后不能为空，
// 保存和比较均使用处理后的文本；重复编号拒绝。
// 编号和名称必须是合法的 UTF-8 文本：任一字段含孤立的 0xFF、0xFE 字节或
// 不完整的多字节序列等无法原样保存的内容时，整次登记以 ErrInvalidText 拒绝，
// 不占用编号，也不改变已有采样点的名称及其关联记录；真正的 U+FFFD（“�”）、
// 中文和文本内部的 U+0000 仍是合法字符。即使另一项为空白，或编号已登记而
// 这次名称含非法字节，也报告文本编码错误。
func (s *Store) RegisterPoint(id, name string) (SamplingPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return SamplingPoint{}, err
	}
	// 在去首尾空白和其它任何处理之前检查原始文本：落盘 JSON 会把非法 UTF-8
	// 静默替换成 U+FFFD，登记“成功”后保存的编号或名称就会与填写内容对不上，
	// 两个不同编号（如末尾分别为孤立 0xFF、0xFE）还可能变成同一编号；只有
	// 整次拒绝才能保证采样点身份在保存前后一致。即使另一项为空白，或编号
	// 已登记而名称含非法字节，也必须报告编码问题。
	if invalidUTF8(id) || invalidUTF8(name) {
		return SamplingPoint{}, ErrInvalidText
	}
	id, name = clean(id), clean(name)
	if id == "" || name == "" {
		return SamplingPoint{}, ErrEmptyField
	}
	if _, ok := s.points[id]; ok {
		return SamplingPoint{}, fmt.Errorf("%w: %s", ErrDuplicatePoint, id)
	}
	p := SamplingPoint{ID: id, Name: name}
	s.points[id] = p
	if err := s.persistLocked(); err != nil {
		delete(s.points, id)
		return SamplingPoint{}, err
	}
	return p, nil
}

// SetLimit 为已登记采样点的某个测量项目登记一版数值上限及生效时间。
// 采样点编号和项目名必须是合法的 UTF-8 文本：任一字段含孤立的 0xFF、0xFE
// 字节或不完整的多字节序列等无法原样保存的内容时，整次登记以 ErrInvalidText
// 拒绝，不新增版本、不占用生效时间；真正的 U+FFFD（“�”）、中文和文本内部的
// U+0000 仍是合法字符。即使同次请求还存在采样点未登记、数值不是有限数或
// 生效时间缺失的问题，也报告文本编码错误。
// 允许乱序补录；生效时间相同（不同时区表示的同一时刻也算相同）的第二条拒绝。
func (s *Store) SetLimit(pointID, item string, value float64, effective time.Time) (Limit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Limit{}, err
	}
	// 在去首尾空白和其它任何处理之前检查原始文本：落盘 JSON 会把非法 UTF-8
	// 静默替换成 U+FFFD，登记“成功”后保存的项目名就会与填写内容对不上，
	// 限值也不再属于实际填写的采样点和项目；只有整次拒绝才能保证一致。
	if invalidUTF8(pointID) || invalidUTF8(item) {
		return Limit{}, ErrInvalidText
	}
	pointID, item = clean(pointID), clean(item)
	if pointID == "" || item == "" {
		return Limit{}, ErrEmptyField
	}
	if _, ok := s.points[pointID]; !ok {
		return Limit{}, fmt.Errorf("%w: %s", ErrUnknownPoint, pointID)
	}
	if !finite(value) {
		return Limit{}, ErrInvalidValue
	}
	if effective.IsZero() {
		return Limit{}, ErrInvalidTime
	}
	lim := Limit{PointID: pointID, Item: item, Value: value, Effective: effective.UTC()}
	g := limitGroup{pointID: pointID, item: item}
	versions := s.limits[g]
	for _, v := range versions {
		if v.Effective.Equal(lim.Effective) {
			return Limit{}, fmt.Errorf("%w: %s/%s @ %s", ErrDuplicateLimitTime, pointID, item, lim.Effective)
		}
	}
	old := s.limits[g]
	// 复制到新切片再插入排序：append/sort 会原地改动共享底层数组，
	// 一旦保存失败需要回滚，old 必须仍是调用前的完整状态。
	versions = make([]Limit, len(old)+1)
	copy(versions, old)
	versions[len(old)] = lim
	sort.Slice(versions, func(i, j int) bool { return versions[i].Effective.Before(versions[j].Effective) })
	s.limits[g] = versions
	if err := s.persistLocked(); err != nil {
		s.limits[g] = old
		return Limit{}, err
	}
	return lim, nil
}

// SubmitSample 录入一份样品，初始为待判定状态。
// 样品编号、采样点编号和每个项目名必须是合法的 UTF-8 文本：任一字段含孤立的
// 0xFF、0xFE 字节或不完整的多字节序列等无法原样保存的内容时，整份提交以
// ErrInvalidText 拒绝，不留部分记录，也不占用编号；真正的 U+FFFD（“�”）
// 仍是合法字符。再次提交同一编号且内容相同（项目排列顺序不同不算变化）返回原样品；
// 任一内容不同则拒绝整个提交，已确认或已作废的样品同样遵守。
func (s *Store) SubmitSample(id, pointID string, sampledAt time.Time, measurements ...Measurement) (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, err
	}
	// 在去首尾空白和其它任何处理之前检查原始文本：落盘 JSON 会把非法 UTF-8
	// 静默替换成 U+FFFD，只有整份拒绝才能保证返回记录、按点查看与保存数据一致。
	// 即使编号已被已有样品占用、非法项目排在最后，或提交同时存在其它内容问题，
	// 也必须报告编码问题：不能归为未知采样点、项目重复或同编号冲突，更不能走
	// 重复录入成功的分支。
	if invalidUTF8(id) || invalidUTF8(pointID) {
		return Sample{}, ErrInvalidText
	}
	for _, m := range measurements {
		if invalidUTF8(m.Item) {
			return Sample{}, ErrInvalidText
		}
	}
	id, pointID = clean(id), clean(pointID)
	if id == "" || pointID == "" {
		return Sample{}, ErrEmptyField
	}
	if sampledAt.IsZero() {
		return Sample{}, ErrInvalidTime
	}
	if len(measurements) == 0 {
		return Sample{}, ErrNoMeasurements
	}
	ms := make([]Measurement, len(measurements))
	seen := make(map[string]float64, len(measurements))
	for i, m := range measurements {
		item := clean(m.Item)
		if item == "" {
			return Sample{}, ErrEmptyField
		}
		if !finite(m.Value) {
			return Sample{}, ErrInvalidValue
		}
		if _, dup := seen[item]; dup {
			return Sample{}, fmt.Errorf("%w: %s", ErrDuplicateItem, item)
		}
		seen[item] = m.Value
		ms[i] = Measurement{Item: item, Value: m.Value}
	}
	sampledAt = sampledAt.UTC()

	if existing, ok := s.samples[id]; ok {
		if sameSampleContent(existing, pointID, sampledAt, seen) {
			return copySample(existing), nil
		}
		return Sample{}, fmt.Errorf("%w: %s", ErrSampleConflict, id)
	}
	if _, ok := s.points[pointID]; !ok {
		return Sample{}, fmt.Errorf("%w: %s", ErrUnknownPoint, pointID)
	}
	smp := &Sample{
		ID:           id,
		PointID:      pointID,
		SampledAt:    sampledAt,
		Measurements: ms,
		Status:       StatusPending,
	}
	s.samples[id] = smp
	if err := s.persistLocked(); err != nil {
		delete(s.samples, id)
		return Sample{}, err
	}
	return copySample(smp), nil
}

func sameSampleContent(s *Sample, pointID string, sampledAt time.Time, ms map[string]float64) bool {
	if s.PointID != pointID || !s.SampledAt.Equal(sampledAt) || len(s.Measurements) != len(ms) {
		return false
	}
	for _, m := range s.Measurements {
		v, ok := ms[m.Item]
		if !ok || v != m.Value {
			return false
		}
	}
	return true
}

// Confirm 确认一份待判定样品：每个项目取生效时间不晚于采样时间的最近一版上限，
// 测量值大于上限才超标，任一项目超标则整份样品超标。
// 只要有项目找不到适用上限，本次确认拒绝，样品保持待判定。
// 重复确认直接返回已保存结果；之后新增或补录限值不改变已确认结果。
func (s *Store) Confirm(id string) (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, err
	}
	id = clean(id)
	if id == "" {
		return Sample{}, ErrEmptyField
	}
	smp, ok := s.samples[id]
	if !ok {
		return Sample{}, fmt.Errorf("%w: %s", ErrUnknownSample, id)
	}
	switch smp.Status {
	case StatusVoided:
		return Sample{}, fmt.Errorf("%w: %s", ErrVoided, id)
	case StatusConfirmed:
		return copySample(smp), nil
	}

	results := make([]ItemResult, 0, len(smp.Measurements))
	exceeded := false
	for _, m := range smp.Measurements {
		lim, ok := applicableLimit(s.limits[limitGroup{pointID: smp.PointID, item: m.Item}], smp.SampledAt)
		if !ok {
			return Sample{}, fmt.Errorf("%w: 样品 %s 的项目 %s", ErrMissingLimit, id, m.Item)
		}
		ex := m.Value > lim.Value
		if ex {
			exceeded = true
		}
		results = append(results, ItemResult{
			Item:           m.Item,
			Value:          m.Value,
			Limit:          lim.Value,
			LimitEffective: lim.Effective,
			Exceeded:       ex,
		})
	}
	smp.Results = results
	smp.Exceeded = exceeded
	smp.Status = StatusConfirmed
	if err := s.persistLocked(); err != nil {
		smp.Results = nil
		smp.Exceeded = false
		smp.Status = StatusPending
		return Sample{}, err
	}
	return copySample(smp), nil
}

// applicableLimit 在按生效时间升序的版本里取不晚于 at 的最近一版；
// 采样时间恰好等于生效时间时使用该版新值。
func applicableLimit(versions []Limit, at time.Time) (Limit, bool) {
	best := -1
	for i, v := range versions {
		if v.Effective.After(at) {
			break
		}
		best = i
	}
	if best < 0 {
		return Limit{}, false
	}
	return versions[best], true
}

// Void 作废样品。原因去掉首尾空白后不能为空；待判定和已确认的样品都能作废。
// 作废后保留测量、原因和已有结果；重复提交处理后相同的原因返回原记录，不同则拒绝。
func (s *Store) Void(id, reason string) (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, err
	}
	id, reason = clean(id), clean(reason)
	if id == "" || reason == "" {
		return Sample{}, ErrEmptyField
	}
	smp, ok := s.samples[id]
	if !ok {
		return Sample{}, fmt.Errorf("%w: %s", ErrUnknownSample, id)
	}
	if smp.Status == StatusVoided {
		if smp.VoidReason == reason {
			return copySample(smp), nil
		}
		return Sample{}, fmt.Errorf("%w: %s", ErrVoidReasonConflict, id)
	}
	oldStatus := smp.Status
	smp.Status = StatusVoided
	smp.VoidReason = reason
	if err := s.persistLocked(); err != nil {
		smp.Status = oldStatus
		smp.VoidReason = ""
		return Sample{}, err
	}
	return copySample(smp), nil
}

// ListByPoint 按采样点查看样品，按采样时间从晚到早排列，同一时刻按样品编号升序。
func (s *Store) ListByPoint(pointID string) ([]Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return nil, err
	}
	return s.pointSamplesLocked(clean(pointID), func(*Sample) bool { return true }), nil
}

// LatestResult 返回该采样点最近有效的已确认样品（跳过待判定与已作废）。
// 没有已确认且未作废的样品时 ok 为 false。
func (s *Store) LatestResult(pointID string) (smp Sample, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, false, err
	}
	list := s.pointSamplesLocked(clean(pointID), func(smp *Sample) bool {
		return smp.Status == StatusConfirmed
	})
	if len(list) == 0 {
		return Sample{}, false, nil
	}
	return list[0], true, nil
}

// pointSamplesLocked 收集某采样点符合 keep 的样品，逐份复制后按统一规则排列，
// 供 ListByPoint 与 LatestResult 共用，保证两种查询对同一批样品的先后判断一致。
// 调用方须已持有锁；pointID 须已做首尾空白处理。
func (s *Store) pointSamplesLocked(pointID string, keep func(*Sample) bool) []Sample {
	var list []Sample
	for _, smp := range s.samples {
		if smp.PointID == pointID && keep(smp) {
			list = append(list, copySample(smp))
		}
	}
	sortSamples(list)
	return list
}

func sortSamples(list []Sample) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if !a.SampledAt.Equal(b.SampledAt) {
			return a.SampledAt.After(b.SampledAt)
		}
		return a.ID < b.ID
	})
}

func copySample(s *Sample) Sample {
	c := *s
	c.Measurements = append([]Measurement(nil), s.Measurements...)
	c.Results = append([]ItemResult(nil), s.Results...)
	return c
}
